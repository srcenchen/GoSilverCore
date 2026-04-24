package receiver

import (
	"fmt"
	"go-silver-core/internal/chunk"
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/gsp_sdk"
	"go-silver-core/pkg/mempool"
	"net"
	"os"
	"sync"
)

func Start(senderAddr string) {
	conn, err := net.Dial("tcp", senderAddr)
	if err != nil {
		panic("发送端连接失败")
	}
	gspC := gsp_sdk.NewGspSdk(conn)
	status, err := gspC.GetFileStatus()
	if err != nil {
		return
	}
	// 创建 文件
	f, err := os.Create("gs-" + status.FileName)
	if err != nil {
		panic("文件创建失败")
	}
	f.Truncate(status.FileSize)
	var wg sync.WaitGroup
	limit := make(chan struct{}, 5)
	mp := mempool.NewMemPool(_const.ChunkSize)
	ck := chunk.NewFileChunk(f, &mp)
	for i := int64(0); i < status.ChunkNum; i++ {
		wg.Add(1)
		limit <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-limit }()
			fmt.Printf("下载 %d / %d 块中...\n", i+1, status.ChunkNum)
			_, err = gspC.GetChunk(senderAddr, i, ck)
			if err != nil {
				panic(err)
			}
		}()
	}
	wg.Wait()
	fmt.Println("下载完毕！")
	os.Exit(0)
}
