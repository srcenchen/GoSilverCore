package receiver

import (
	"fmt"
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/gsp_sdk/client"
	"go-silver-core/internal/gsp_sdk/server"
	"go-silver-core/pkg/mempool"
	"math/rand/v2"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

func Start(senderAddr string) {
	// 1. 初始化内存池和基础 Session
	mp := mempool.NewMemPool(_const.ChunkSize)
	peerPort := rand.IntN(999) + 3000
	s := server.NewGspSession(":"+strconv.Itoa(peerPort), mp)
	s.Start()

	// 2. 初始化 SDK 并获取文件信息
	gspC := client.NewGspSdk(senderAddr, mp)
	status, err := gspC.GetFileStatus()
	if err != nil {
		fmt.Printf("无法获取文件状态: %v\n", err)
		return
	}

	// 3. 初始化进度条容器
	// 所有发往 p 的内容都会被置于进度条上方
	p := mpb.New(
		mpb.WithWidth(64),
		mpb.WithOutput(os.Stderr), // 进度条通常输出到标准错误流
	)

	// 4. 创建进度条实例
	bar := p.AddBar(int64(status.ChunkNum),
		mpb.PrependDecorators(
			decor.Name("下载中: "),
			// 使用 WC 结构代替 W6
			decor.Percentage(decor.WC{W: 6}),
		),
		mpb.AppendDecorators(
			decor.OnComplete(
				// 修正：ETA 使用 ET_STYLE_GO 或 ET_STYLE_HHMMSS
				decor.EwmaETA(decor.ET_STYLE_GO, 60), "完成!",
			),
		),
	)

	// 准备本地文件
	f, err := os.Create("gs-" + status.FileName)
	if err != nil {
		fmt.Fprintf(p, "文件创建失败: %v\n", err)
		return
	}
	defer f.Close()
	f.Truncate(status.FileSize)
	s.BeSendSub(f)
	ck := s.GetChunk()

	// Peer 注册
	if err := gspC.PeerReg(peerPort, s.UUID); err != nil {
		fmt.Fprintf(p, "服务端连接失败: %v\n", err)
		return
	}

	// 准备分块索引
	indices := make([]int64, status.ChunkNum)
	for i := range indices {
		indices[i] = int64(i)
	}

	// 随机打乱分块顺序，优化 P2P 分发效率
	rand.Shuffle(len(indices), func(i, j int) {
		indices[i], indices[j] = indices[j], indices[i]
	})

	// ---------------------------------
	// 主循环：直到所有块下载成功
	for len(indices) > 0 {
		var mu sync.Mutex
		var failedList []int64
		var wg sync.WaitGroup
		limit := make(chan struct{}, 5) // 控制并发数

		for _, idx := range indices {
			wg.Add(1)
			limit <- struct{}{}

			go func(i int64) {
				defer wg.Done()
				defer func() { <-limit }()

				// ⚠️ 关键点：使用 fmt.Fprintf(p, ...) 代替 fmt.Printf
				// 这会通知 mpb 重新计算进度条位置，确保日志不覆盖条
				fmt.Fprintf(p, "[任务] 正在申请第 %d / %d 块...\n", i+1, status.ChunkNum)

				// 询问 Tracker 块位置
				reChunk, err := gspC.WantChunk(i)
				if err != nil {
					fmt.Fprintf(p, "[警告] 请求块 %d 失败: %v\n", i, err)
					mu.Lock()
					failedList = append(failedList, i)
					mu.Unlock()
					return
				}

				targetAddr := reChunk.Addr
				if targetAddr == "" {
					targetAddr = senderAddr
				}

				// 开始下载
				tBegin := time.Now()
				_, cm, err := gspC.GetChunk(targetAddr, i, &ck)
				if err != nil {
					fmt.Fprintf(p, "[错误] 从 %s 下载块 %d 失败: %v\n", targetAddr, i, err)
					// 上报失败，以扣减提供端的并发连接数
					_ = gspC.ReportPeer(s.UUID, reChunk.UUID, 0, "failed")
					mu.Lock()
					failedList = append(failedList, i)
					mu.Unlock()
					return
				}

				// 计算下载速度 (字节/微秒 * 8 = Mb/s)
				duration := time.Since(tBegin).Microseconds()
				var speedMbps int64 = 0
				if duration > 0 {
					speedMbps = (_const.ChunkSize / duration) * 8
				}

				fmt.Fprintf(p, "[成功] 块 %d 下载完毕 | 速度: %d Mb/s | 来自: %s\n", i, speedMbps, targetAddr)

				// 5. 更新进度条状态
				bar.Increment()

				// 上报状态
				s.AddChunk(i, cm)
				gspC.ReportChunk(s.UUID, i)
				gspC.ReportPeer(s.UUID, reChunk.UUID, speedMbps, "done")
			}(idx)
		}
		wg.Wait()
		indices = failedList // 如果有失败的块，进入下一轮重试
	}

	// 6. 确保进度条渲染完成并退出渲染循环
	p.Wait()
	fmt.Println("\n🎉 下载任务已圆满完成！")
	select {}
}
