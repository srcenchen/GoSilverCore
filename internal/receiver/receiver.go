package receiver

import (
	"encoding/json"
	"fmt"
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/gsp"
	"go-silver-core/internal/gsp_sdk"
	"go-silver-core/internal/gsp_sdk/model"
	"go-silver-core/pkg/mempool"
	"math/rand/v2" // 使用 v2 更快更现代
	"net"
	"os"
	"sync"
)

// AI制作的随机模式
func Start(senderAddr string) {
	mp := mempool.NewMemPool(_const.ChunkSize)
	s := gsp_sdk.NewGspSession(":48081", &mp)
	s.Start()
	gspC := gsp_sdk.NewGspSdk(senderAddr)
	status, err := gspC.GetFileStatus()
	if err != nil {
		return
	}
	f, err := os.Create("gs-" + status.FileName)
	if err != nil {
		panic("文件创建失败")
	}
	f.Truncate(status.FileSize)
	s.BeSendSub(f)
	ck := s.GetChunk()

	// 开一条Peer控制流
	controlConn, err := net.Dial("tcp", senderAddr)
	if err != nil {
		panic("连接服务端失败")
	}
	codec := gsp.Codec{}
	jsonReq, _ := json.Marshal(model.PeerRegReq{
		Operate: "peerReg",
		Port:    "48081",
	})
	reqReg := codec.Encode(gsp.TypeJSON, jsonReq)
	controlConn.Write(reqReg)
	// --- 核心优化：生成并打乱索引序列 ---
	indices := make([]int64, status.ChunkNum)
	for i := range indices {
		indices[i] = int64(i)
	}

	// 使用 PCG 算法打乱，保证每个节点的起始分块大概率不同
	rand.Shuffle(len(indices), func(i, j int) {
		indices[i], indices[j] = indices[j], indices[i]
	})
	// ---------------------------------

	var wg sync.WaitGroup
	limit := make(chan struct{}, 5)

	for _, idx := range indices {
		//idx := 1
		wg.Add(1)
		limit <- struct{}{}

		// 这里的 idx 是作为参数传入，避免闭包捕获变量的坑（虽然 Go 1.22 已修复，但习惯要好）
		go func(i int64) {
			defer wg.Done()
			defer func() { <-limit }()

			fmt.Printf("申请 %d / %d 块中...\n", i+1, status.ChunkNum)

			// 向 Tracker/主服务器询问谁有这个块
			reChunk, err := gspC.WantChunk(i)
			if err != nil {
				// 实际项目中这里建议增加重试，而不是直接 panic
				fmt.Printf("警告：请求块 %d 失败: %v\n", i, err)
				return
			}

			// 如果没人有，则回退到主服务器
			targetAddr := reChunk.Addr
			if targetAddr == "" {
				targetAddr = senderAddr
			}

			fmt.Printf("申请 %d / %d 块成功，对端地址 %s 下载中...\n", i+1, status.ChunkNum, targetAddr)

			_, cm, err := gspC.GetChunk(targetAddr, i, &ck)
			if err != nil {
				fmt.Printf("错误：从 %s 下载块 %d 失败: %v\n", targetAddr, i, err)
				return
			}

			// 更新本地状态并上报，让别人能发现我有这个块
			s.AddChunk(i, cm)
			gspC.ReportChunk("48081", i)
		}(int64(idx))
	}
	wg.Wait()
	fmt.Println("下载完毕！")
}
