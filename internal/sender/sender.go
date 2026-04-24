package sender

import (
	"go-silver-core/internal/gsp_sdk"
	"log/slog"
	"os"
)

func Start(filePath string) {
	// 启动sender服务端（主节点）
	s := gsp_sdk.NewGspSession(":18080")
	s.Start()
	f, err := os.Open("filePath")
	if err != nil {
		slog.Error(filePath + "不存在")
		os.Exit(0)
	}
	s.BeSendMain(f)
}
