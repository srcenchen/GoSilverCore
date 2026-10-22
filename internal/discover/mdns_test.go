package discover

import (
	"fmt"
	"go-silver-core/pkg/gosilver"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoveryIgnoresStaleRecordsAndCollapsesInstanceAddresses(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	s := gosilver.NewServer("127.0.0.1:0", file)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	addr := fmt.Sprintf("127.0.0.1:%d", s.Port())
	found := map[string]map[string]struct{}{"stopped-instance": {"127.0.0.1:1": {}}, "current-instance": {addr: {}, "127.0.0.1:2": {}}}
	got, err := liveSender(found)
	if err != nil || got != addr {
		t.Fatalf("stale discovery record used: %s %v", got, err)
	}
}
