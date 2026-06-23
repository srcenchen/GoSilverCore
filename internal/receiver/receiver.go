package receiver

import (
	"fmt"
	_const "go-silver-core/internal/const"
	"go-silver-core/internal/gsp_sdk/client"
	"go-silver-core/internal/gsp_sdk/model"
	"go-silver-core/internal/gsp_sdk/server"
	"go-silver-core/pkg/gosilver"
	"go-silver-core/pkg/mempool"
	"math/rand/v2"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

func Start(senderAddr string, seed bool, zone string) {
	// 1. 初始化内存池和基础 Session
	mp := mempool.NewMemPool(_const.ChunkSize)
	peerPort := rand.IntN(999) + 3000
	s := server.NewGspSession(":"+strconv.Itoa(peerPort), mp)
	s.Start()

	// 2. 初始化 SDK 并获取文件信息
	gspC := client.NewGspSdk(senderAddr, mp, s.UUID, zone)
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

	// 准备本地文件：清洗远端文件名，防止 ../ 路径穿越/非法字符（与 pkg/gosilver 行为一致）。
	f, err := os.Create("gs-" + gosilver.SanitizeFileName(status.FileName))
	if err != nil {
		fmt.Fprintf(p, "文件创建失败: %v\n", err)
		return
	}
	defer f.Close()
	f.Truncate(status.FileSize)
	s.BeSendSub(f)
	ck := s.GetChunk()

	// 控制面处理器：响应中心的统一指挥（prefetch 补种 / backoff 降并发）。
	onControl := func(msg model.ControlMsg) {
		switch msg.Cmd {
		case "prefetch":
			if s.HasLocalChunk(msg.Index) {
				return
			}
			re, err := gspC.WantChunk(msg.Index, _const.SchedMaxSourcesPerWant)
			if err != nil {
				return
			}
			if cm, _, _, ok := gspC.FetchChunk(msg.Index, &ck, re.Sources, senderAddr, nil); ok {
				s.AddChunk(msg.Index, cm)
				_ = gspC.ReportChunk(s.UUID, msg.Index)
				fmt.Fprintf(p, "[指挥] 已按中心指令预取块 %d 补种\n", msg.Index)
			}
		case "backoff":
			s.SetUploadMax(_const.UploadConcurrencyHDD)
			go func(until int64) {
				if d := time.Until(time.Unix(until, 0)); d > 0 {
					time.Sleep(d)
				}
				s.SetUploadMax(_const.UploadConcurrencyDefault)
			}(msg.Until)
		}
	}

	// Peer 注册
	if err := gspC.PeerReg(peerPort, s.UUID, onControl); err != nil {
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

				// 询问调度中心，拿到按吞吐排序的候选源列表
				reChunk, err := gspC.WantChunk(i, _const.SchedMaxSourcesPerWant)
				if err != nil {
					fmt.Fprintf(p, "[警告] 请求块 %d 失败: %v\n", i, err)
					mu.Lock()
					failedList = append(failedList, i)
					mu.Unlock()
					return
				}

				// 按候选列表本地故障转移：首选失败立即试次选，不再整轮重试。
				cm, speedMbps, usedAddr, ok := gspC.FetchChunk(i, &ck, reChunk.Sources, senderAddr, nil)
				if !ok {
					fmt.Fprintf(p, "[错误] 块 %d 所有候选源均失败\n", i)
					mu.Lock()
					failedList = append(failedList, i)
					mu.Unlock()
					return
				}

				fmt.Fprintf(p, "[成功] 块 %d 下载完毕 | 速度: %d Mb/s | 来自: %s\n", i, speedMbps, usedAddr)

				// 5. 更新进度条状态
				bar.Increment()

				// 上报状态：先本地登记可供他人下载，再告知中心
				s.AddChunk(i, cm)
				gspC.ReportChunk(s.UUID, i)
			}(idx)
		}
		wg.Wait()
		indices = failedList // 如果有失败的块，进入下一轮重试
	}

	// 6. 确保进度条渲染完成并退出渲染循环
	p.Wait()
	fmt.Println("\n🎉 下载任务已圆满完成！")
	if seed {
		fmt.Println("正在作为做种节点运行... (按 Ctrl+C 退出)")
		select {}
	} else {
		// Stop session before exiting
		s.Stop()
	}
}
