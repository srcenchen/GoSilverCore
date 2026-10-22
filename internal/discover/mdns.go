package discover

import (
	"context"
	"errors"
	"fmt"
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/gsp_sdk/client"
	"go-silver-core/pkg/mempool"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/mdns"
)

const Service = "_gosilver._tcp"

func Publish(port int, id string) (*mdns.Server, error) {
	ip, err := outboundIP()
	if err != nil {
		return nil, err
	}
	svc, err := mdns.NewMDNSService("gosilver-"+id[:min(8, len(id))], Service, "", "", port, []net.IP{ip}, []string{"id=" + id})
	if err != nil {
		return nil, err
	}
	return mdns.NewServer(&mdns.Config{Zone: svc})
}

func LookupSender(timeout time.Duration, selfID string) (string, error) {
	entries := make(chan *mdns.ServiceEntry, 256)
	result := make(chan error, 1)
	go func() {
		params := mdns.DefaultParams(Service)
		params.Timeout = timeout
		params.Entries = entries
		params.DisableIPv6 = true
		params.Logger = log.New(io.Discard, "", 0)
		result <- mdns.Query(params)
	}()
	found := map[string]map[string]struct{}{}
	var pending []*mdns.ServiceEntry
	collect := func(e *mdns.ServiceEntry) {
		if e == nil || e.AddrV4 == nil || e.Port == 0 || entryID(e) == "" || selfID != "" && entryID(e) == selfID {
			return
		}
		id := entryID(e)
		if found[id] == nil {
			found[id] = map[string]struct{}{}
		}
		found[id][net.JoinHostPort(e.AddrV4.String(), strconv.Itoa(e.Port))] = struct{}{}
	}
	finish := func(err error) (string, error) {
		for {
			select {
			case e := <-entries:
				pending = append(pending, e)
			default:
				goto drained
			}
		}
	drained:
		for _, e := range pending {
			collect(e)
		}
		if len(found) > 0 {
			return liveSender(found)
		}

		if err != nil {
			return "", err
		}
		return "", errors.New("未发现发送端")
	}
	// The library mutates published ServiceEntry pointers while querying. Store
	// pointers without reading their fields until Query returns; then drain them. Selecting the
	// completion channel before the final entry must not turn discovery into EOF.
	for {
		select {
		case e := <-entries:
			pending = append(pending, e)
		case err := <-result:
			return finish(err)
		}
	}
}

func HasSender(timeout time.Duration, selfID string) (string, bool) {
	addr, err := LookupSender(timeout, selfID)
	return addr, err == nil && addr != ""
}

func entryID(e *mdns.ServiceEntry) string {
	for _, f := range e.InfoFields {
		if len(f) > 3 && f[:3] == "id=" {
			return f[3:]
		}
	}
	return ""
}

func outboundIP() (net.IP, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			ip, _, e := net.ParseCIDR(addr.String())
			if e == nil && ip.To4() != nil && ip.IsPrivate() {
				return ip, nil
			}
		}
	}
	return nil, errors.New("未找到局域网 IPv4 地址")
}

// mDNS records can outlive a stopped process. Validate readiness and collapse
// addresses belonging to the same instance before reporting multiple senders.
func liveSender(found map[string]map[string]struct{}) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	live := map[string]string{}
	for id, addresses := range found {
		for addr := range addresses {
			wg.Add(1)
			go func(id, addr string) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-sem }()
				sdk := client.NewGspSdk(addr, mempool.NewMemPool(_const.ChunkSize)).WithContext(ctx)
				defer sdk.Close()
				if _, err := sdk.GetNodeStatus(); err == nil {
					mu.Lock()
					if live[id] == "" {
						live[id] = addr
					}
					mu.Unlock()
				}
			}(id, addr)
		}
	}
	wg.Wait()
	if len(live) > 1 {
		return "", fmt.Errorf("发现 %d 个在线发送端，请使用手动地址直连", len(live))
	}
	for _, addr := range live {
		return addr, nil
	}
	return "", errors.New("发现记录对应的发送端已离线或尚未就绪，请重试或手动直连")
}
