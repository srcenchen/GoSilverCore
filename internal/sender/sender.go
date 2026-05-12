package sender

import (
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/gsp_sdk/server"
	"go-silver-core/pkg/mempool"
	"log"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"
)

func Start(filePath string) {
	// 启动sender服务端（主节点）
	go func() {
		log.Println("Starting pprof debug server on 0.0.0.0:6060")
		if err := http.ListenAndServe("0.0.0.0:6060", nil); err != nil {
			log.Fatalf("pprof server failed: %v", err)
		}
	}()
	mp := mempool.NewMemPool(_const.ChunkSize)
	s := server.NewGspSession(":48080", mp)
	s.Start()
	f, err := os.Open(filePath)
	if err != nil {
		slog.Error(filePath + "不存在")
		os.Exit(0)
	}
	s.BeSendMain(f)
}
