package sender

import (
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/gsp_sdk"
	"go-silver-core/pkg/mempool"
	"log/slog"
	"os"
)

func Start(filePath string) {
	// 启动sender服务端（主节点）
	mp := mempool.NewMemPool(_const.ChunkSize)
	s := gsp_sdk.NewGspSession(":48080", &mp)
	s.Start()
	f, err := os.Open(filePath)
	if err != nil {
		slog.Error(filePath + "不存在")
		os.Exit(0)
	}
	s.BeSendMain(f)
}
