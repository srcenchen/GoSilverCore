# ICPC 集控系统集成与使用指南

本项目（GoSilver-Core）已被封装为一个标准的 Go 语言库（包名：`gosilver`），提供简单、非阻塞且支持实时进度反馈的 API。它非常适合集成到 ICPC 集控系统中，用于批量大文件（如比赛题目包、环境镜像、评测插件）的局域网高效分发。

本指南将介绍如何引入该库，并通过具体的代码示例展示其用法。

---

## 一、 快速引入

在 ICPC 集控系统项目根目录下，直接通过本地相对路径或 Go Module 路径引入：

```go
import "go-silver-core/pkg/gosilver"
```

---

## 二、 核心 API 结构

### 1. 进度信息模型 (`ProgressInfo`)
用于实时反馈下载进度，集控系统可根据这些字段更新前端 UI（如进度条、当前速度等）：

```go
type ProgressInfo struct {
    TotalChunks int64   // 该文件的总分块数 (每个分块默认为 8MB)
    Downloaded  int64   // 当前已完成下载的分块数
    Percentage  float64 // 下载百分比 (0.0 到 100.0)
    SpeedMbps   int64   // 当前下载速度 (Mbps)
    Status      string  // 状态值:
                        // "idle": 初始闲置状态
                        // "downloading": 正在下载中
                        // "completed": 成功完成下载
                        // "failed": 下载失败 (参见 Error 字段)
                        // "cancelled": 被用户取消
    Error       error   // 发生错误时的具体 Error 对象
}
```

---

## 三、 使用场景与代码示例

### 1. 服务端 (Sender) —— 在主控机上分发文件
在主控机（或题库服务器）上启动服务端，向所有参赛选手机或评测机发布文件源：

```go
package main

import (
	"log"
	"time"

	"go-silver-core/pkg/gosilver"
)

func main() {
	// 1. 初始化服务端：监听地址为 ":48080"，要分发的文件为当前目录下的 "contest_problems.zip"
	server := gosilver.NewServer(":48080", "./contest_problems.zip")

	// 2. 启动服务（非阻塞，已在后台开启 TCP 监听）
	log.Println("正在启动文件分发服务...")
	if err := server.Start(); err != nil {
		log.Fatalf("启动服务失败: %v", err)
	}
	log.Println("服务启动成功，等待客户端连接下载...")

	// 模拟主控机运行一段时间 (例如比赛期间持续提供下载)
	time.Sleep(2 * time.Hour)

	// 3. 停止服务（释放文件句柄和网络端口监听）
	log.Println("比赛结束，停止文件分发服务...")
	server.Stop()
}
```

---

### 2. 客户端 (Receiver) —— 在选手机/评测机上下载并参与 P2P 共享
客户端用于下载文件。在下载过程中，它不仅会汇报进度，**下载完成的分块还会自动提供给局域网内的其他客户端**，分担主控服务器的带宽压力。

```go
package main

import (
	"fmt"
	"log"
	"time"

	"go-silver-core/pkg/gosilver"
)

func main() {
	// 1. 初始化客户端
	// 参数1：主控机服务端地址
	// 参数2：保存目录 (空表示当前目录，下载的文件会自动命名为 "gs-文件名")
	client := gosilver.NewClient("192.168.1.100:48080", "./downloads")

	// 2. 启动下载命令 (非阻塞)
	progressCh, err := client.StartDownload()
	if err != nil {
		log.Fatalf("启动下载失败: %v", err)
	}
	log.Println("文件下载已启动...")

	// 3. 实时监听进度反馈通道
	// 此循环可放置于独立的 Goroutine 中，将进度推送到集控系统的消息总线（例如 WebSocket/MQTT/gRPC）
	for progress := range progressCh {
		switch progress.Status {
		case "downloading":
			fmt.Printf("\r进度: %.2f%% | 已下载分块: %d/%d | 速度: %d Mbps", 
				progress.Percentage, progress.Downloaded, progress.TotalChunks, progress.SpeedMbps)
		case "completed":
			fmt.Println("\n🎉 文件下载并校验成功！")
			return
		case "failed":
			log.Fatalf("\n❌ 下载失败: %v", progress.Error)
			return
		case "cancelled":
			log.Println("\n⚠️ 下载已被集控系统取消")
			return
		}
	}
}
```

---

### 3. 取消/中断下载 (例如集控系统发出停止指令)
当集控系统管理员在控制后台点击了“取消分发”或“停止同步”时，可以随时调用 `CancelDownload()`：

```go
// 在另一个协程中，收到集控系统总线的停止信号时：
func onCancelSignalReceived(client *gosilver.Client) {
    log.Println("收到集控系统停止指令，正在取消下载并清理资源...")
    
    // 该方法会向下载协程发送取消信号，并同步阻塞等待其完成磁盘文件和网络端口的关闭
    client.CancelDownload()
    
    log.Println("资源清理完毕，下载已成功中断。")
}
```

---

## 四、 在 ICPC 集控系统中的集成建议

1. **状态轮询与广播**：
   建议将 `client.StartDownload()` 返回的 `progressCh` 通道读取器集成在集控客户端的服务进程中。一旦读取到状态更新（如每下载 1% 或速度改变），便通过局域网通信协议（如 MQTT 或 HTTP Post）上报给集控中心，使比赛大屏或管理员后台能实时渲染各个参赛队机器的题目包同步状态。
   
2. **随机端口防冲突**：
   每个 `Client` 在启动时会自动使用 `rand.IntN(999) + 3000` 随机一个局域网端口（例如 3000 到 3999）作为本地 GSP 服务的监听端口。这保证了在单台多卡评测机或开发测试机上，同时启动多个 `Client` 时不会发生端口冲突错误。
   
3. **分发完整性保障**：
   下载结束后，本库已自动完成各块的 CRC32 校验，因此无需在集控层重复手动计算 MD5 或 SHA-256。
