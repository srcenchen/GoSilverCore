// Package discovery 提供局域网内的自动发现 / 推送机制。
//
// 发送端（主控）通过 Announce 周期性向一个 IPv4 本地子网多播组广播分发通告，
// 空闲的接收端通过 Listen 监听该多播组，收到通告后即可自动开始下载。
//
// 实现仅依赖 Go 标准库 net，使用多播（而非需要 SO_BROADCAST 套接字选项的广播），
// 因此在 macOS / Windows / Linux 上行为一致，无需安装任何第三方软件或系统守护进程。
// 多播 TTL 默认为 1，报文不会越过路由器，天然局限在本地局域网内，契合机房/比赛集控场景。
package discovery

import (
	"encoding/json"
	"net"
	"strconv"
	"time"
)

const (
	// Magic 协议标识，用于过滤掉多播组里无关的 UDP 流量。
	Magic = "gsp-discovery/1"

	// multicastGroup IPv4 本地子网多播组地址（239.0.0.0/8 为管理性局域网多播段）。
	multicastGroup = "239.255.41.81"

	// Port 发现服务使用的 UDP 端口。
	Port = 48081

	// defaultInterval 默认广播间隔。
	defaultInterval = 2 * time.Second
)

var groupAddr = &net.UDPAddr{IP: net.ParseIP(multicastGroup), Port: Port}

// AnnounceInfo 是一条分发通告的内容。
type AnnounceInfo struct {
	Magic    string `json:"magic"`    // 协议标识
	Port     int    `json:"port"`     // 发送端 GSP 监听端口（例如 48080）
	FileName string `json:"fileName"` // 待分发文件名
	FileSize int64  `json:"fileSize"` // 文件大小（字节）
	SaveDir  string `json:"saveDir"`  // 发送端指定的客户端保存目录（可空，空表示由客户端自行决定）
	Token    string `json:"token"`    // 本次分发会话标识，供接收端去重
}

// SenderAddr 根据通告内容与收到该报文的源地址，组装出可直接拨号的发送端 GSP 地址。
// 发送端无需预先知道自己的局域网 IP —— 由接收端从 UDP 源地址推导，更可靠。
func (a AnnounceInfo) SenderAddr(src net.Addr) string {
	host := ""
	if u, ok := src.(*net.UDPAddr); ok && u.IP != nil {
		host = u.IP.String()
	}
	return net.JoinHostPort(host, strconv.Itoa(a.Port))
}

// validIPv4 返回该网卡上的一个合法 IPv4 地址，如果没有则返回 nil
func validIPv4(ifi *net.Interface) net.IP {
	addrs, _ := ifi.Addrs()
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip != nil && ip.To4() != nil && !ip.IsLoopback() {
			return ip.To4()
		}
	}
	return nil
}

// getMulticastInterfaces 获取所有支持多播且具有 IPv4 地址并且处于 UP 状态的网卡
func getMulticastInterfaces() []*net.Interface {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var validIfaces []*net.Interface
	for i := range ifaces {
		ifi := &ifaces[i]
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 {
			continue
		}
		if validIPv4(ifi) != nil {
			validIfaces = append(validIfaces, ifi)
		}
	}
	return validIfaces
}

// Announce 周期性向局域网广播分发通告，直到 stop 被关闭。
// interval <= 0 时使用默认 2s。该函数立即返回，广播在后台协程中进行。
func Announce(stop <-chan struct{}, info AnnounceInfo, interval time.Duration) error {
	if interval <= 0 {
		interval = defaultInterval
	}
	info.Magic = Magic
	payload, err := json.Marshal(info)
	if err != nil {
		return err
	}

	ifaces := getMulticastInterfaces()
	var conns []*net.UDPConn

	for _, ifi := range ifaces {
		ip := validIPv4(ifi)
		if ip == nil {
			continue
		}
		// 绑定到网卡的具体 IP，实现多网卡分别发送
		laddr := &net.UDPAddr{IP: ip, Port: 0}
		conn, err := net.DialUDP("udp4", laddr, groupAddr)
		if err == nil {
			conns = append(conns, conn)
		}
	}

	// 如果没有找到合适的网卡或者均失败，退化为系统默认路由发送
	if len(conns) == 0 {
		conn, err := net.DialUDP("udp4", nil, groupAddr)
		if err != nil {
			return err
		}
		conns = append(conns, conn)
	}

	go func() {
		defer func() {
			for _, c := range conns {
				c.Close()
			}
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for _, c := range conns {
			_, _ = c.Write(payload) // 立即先发一帧，便于接收端尽快感知
		}
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				for _, c := range conns {
					_, _ = c.Write(payload)
				}
			}
		}
	}()
	return nil
}

// Listen 监听局域网内的分发通告，每收到一条合法通告就调用 handler。
// handler 在后台协程中被调用，第二个参数是报文源地址（用于 AnnounceInfo.SenderAddr）。
// 关闭 stop 即可停止监听并释放套接字。该函数立即返回。
func Listen(stop <-chan struct{}, handler func(AnnounceInfo, net.Addr)) error {
	ifaces := getMulticastInterfaces()
	if len(ifaces) == 0 {
		// 退化为系统默认多播接口监听
		return listenOnInterface(nil, stop, handler)
	}

	var successCount int
	for _, ifi := range ifaces {
		err := listenOnInterface(ifi, stop, handler)
		if err == nil {
			successCount++
		}
	}

	if successCount == 0 {
		// 如果指定网卡全部失败，尝试退化
		return listenOnInterface(nil, stop, handler)
	}
	return nil
}

func listenOnInterface(ifi *net.Interface, stop <-chan struct{}, handler func(AnnounceInfo, net.Addr)) error {
	conn, err := net.ListenMulticastUDP("udp4", ifi, groupAddr)
	if err != nil {
		return err
	}
	_ = conn.SetReadBuffer(64 * 1024)
	go func() {
		defer conn.Close()
		buf := make([]byte, 2048)
		for {
			select {
			case <-stop:
				return
			default:
			}
			// 带超时读，保证 stop 关闭后能及时退出循环
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
				// 其余错误：若已被要求停止则退出，否则继续尝试
				select {
				case <-stop:
					return
				default:
					continue
				}
			}
			var info AnnounceInfo
			if json.Unmarshal(buf[:n], &info) != nil || info.Magic != Magic {
				continue
			}
			handler(info, src)
		}
	}()
	return nil
}
