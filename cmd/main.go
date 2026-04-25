package main

import (
	"flag"
	"go-silver-core/internal/receiver"
	"go-silver-core/internal/sender"
	"log"
)

var (
	mode       string
	filePath   string
	senderAddr string
)

func init() {
	flag.StringVar(&mode, "mode", "sender", "模式选择，receiver/sender")
	flag.StringVar(&filePath, "file", "/Users/sanenchen/GoSilver/10g.file", "文件路径")
	flag.StringVar(&senderAddr, "senderAddr", "127.0.0.1:18080", "发送端地址，类似 192.168.1.10:18080")
}
func main() {
	flag.Parse()
	// 接收端模式和发送端模式
	if mode == "receiver" {
		log.Println("接收模式")
		receiver.Start(senderAddr)
	} else if mode == "sender" {
		log.Println("发送模式")
		sender.Start(filePath)
	} else {
		panic("模式选择错误")
	}
	select {}
}
