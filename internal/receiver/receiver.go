package receiver

import (
	"io"
	"log"
	"net"
)

func Start() {
	conn, err := net.Dial("tcp", "localhost:38080")
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)

		if err != nil {
			if err == io.EOF {
				panic("连接关闭")
			}
		}
		data := buf[:n]
		log.Printf("received: %s", string(data))
	}
}
