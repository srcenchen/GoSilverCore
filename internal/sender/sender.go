package sender

import (
	"net"
	"strconv"
	"time"
)

func Start() {
	lis, err := net.ListenTCP("tcp", &net.TCPAddr{
		Port: 38080,
	})
	if err != nil {
		panic("启动端口监听失败" + err.Error())
	}
	defer lis.Close()
	for {
		conn, err := lis.Accept()
		if err != nil {
			panic(err)
		}
		go handleConn(conn)

	}
}

func handleConn(conn net.Conn) {
	defer conn.Close()
	// 连续发送 1000 次当前时间戳，不加任何延迟
	for i := 0; i < 1000; i++ {
		// 发送像 "1713873000" 这样的字符串
		data := strconv.FormatInt(time.Now().Unix(), 10)
		_, err := conn.Write([]byte(data + "\n"))
		if err != nil {
			return
		}
	}
}
