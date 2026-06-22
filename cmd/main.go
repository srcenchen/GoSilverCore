package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"

	"go-silver-core/internal/receiver"
	"go-silver-core/internal/sender"
	"go-silver-core/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
)

var (
	mode       string
	filePath   string
	senderAddr string
)

func init() {
	flag.StringVar(&mode, "mode", "sender", "模式选择 receiver/sender（提供任一参数即进入非交互模式）")
	flag.StringVar(&filePath, "file", "", "文件路径（sender 模式）")
	flag.StringVar(&senderAddr, "senderAddr", "", "发送端地址，例如 192.168.1.10:48080（receiver 模式）")
}

func main() {
	flag.Parse()

	// 未显式提供任何参数 -> 进入 TUI 壳：默认接收模式，按 s 切换发送模式。
	if flag.NFlag() == 0 {
		originalStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w
		os.Stderr = w
		log.SetOutput(w)

		go func() {
			scanner := bufio.NewScanner(r)
			for scanner.Scan() {
				select {
				case tui.GlobalLogCh <- scanner.Text():
				default:
				}
			}
		}()

		p := tea.NewProgram(tui.New(), tea.WithAltScreen(), tea.WithOutput(originalStdout))
		if _, err := p.Run(); err != nil {
			fmt.Fprintf(originalStdout, "TUI 运行失败: %v\n", err)
		}
		return
	}

	// 兼容旧的非交互用法（供脚本 / 集控系统直接调用，对外行为不变）。
	switch mode {
	case "receiver":
		if senderAddr == "" {
			fmt.Println("receiver 模式需要 -senderAddr")
			os.Exit(2)
		}
		log.Println("接收模式")
		receiver.Start(senderAddr)
	case "sender":
		if filePath == "" {
			fmt.Println("sender 模式需要 -file")
			os.Exit(2)
		}
		log.Println("发送模式")
		sender.Start(filePath)
	default:
		log.Fatal("模式选择错误")
	}
	select {}
}
