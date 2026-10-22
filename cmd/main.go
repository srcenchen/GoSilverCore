package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"go-silver-core/internal/webui"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	var config webui.Config
	flag.StringVar(&config.Mode, "mode", "", "预选模式: sender / receiver")
	flag.StringVar(&config.File, "file", "", "发送文件或文件夹路径（配合 -mode=sender 自动准备）")
	flag.StringVar(&config.SenderAddr, "senderAddr", "", "发送端地址（配合 -mode=receiver 自动连接）")
	flag.StringVar(&config.Listen, "listen", ":0", "发送端 TCP 监听地址")
	flag.StringVar(&config.Token, "token", os.Getenv("GOSILVER_TOKEN"), "接收端控制密钥，也可设置 GOSILVER_TOKEN")
	flag.BoolVar(&config.AllowCommands, "allowCommands", true, "接收端允许命令执行（默认开启）")
	uiAddr := flag.String("webAddr", "127.0.0.1:0", "本机 WebUI 地址，端口 0 自动分配")
	noBrowser := flag.Bool("noBrowser", false, "不自动打开浏览器")
	flag.Parse()
	host, _, err := net.SplitHostPort(*uiAddr)
	if err != nil {
		return err
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("webAddr 必须绑定本机回环地址")
		}
	}
	app, err := webui.New(config)
	if err != nil {
		return err
	}
	defer app.Close()
	listener, err := net.Listen("tcp", *uiAddr)
	if err != nil {
		return fmt.Errorf("WebUI 启动失败，可用 -webAddr=127.0.0.1:0 自动选择端口: %w", err)
	}
	srv := &http.Server{Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 64 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	failures := make(chan error, 1)
	go func() { failures <- srv.Serve(listener) }()
	url := "http://" + listener.Addr().String()
	fmt.Println("GoSilver WebUI:", url)
	if err = app.AutoStart(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	if !*noBrowser {
		openBrowser(url)
	}
	select {
	case <-ctx.Done():
	case err = <-failures:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	app.Close()
	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err == nil {
		go func() { _ = cmd.Wait() }()
	}
}
