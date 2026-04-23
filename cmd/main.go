package main

import (
	"flag"
	"go-silver-core/internal/receiver"
	"go-silver-core/internal/sender"
	"log"
)

var (
	mode string
)

func init() {
	flag.StringVar(&mode, "mode", "sender", "模式选择，receiver/sender")
}
func main() {
	flag.Parse()
	// 接收端模式和发送端模式
	if mode == "receiver" {
		log.Println("接收模式")
		receiver.Start()
	} else if mode == "sender" {
		log.Println("发送模式")
		sender.Start()
	} else {
		panic("模式选择错误")
	}
}
