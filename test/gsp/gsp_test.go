package gsp

import (
	"go-silver-core/pkg/chunk"
	"go-silver-core/pkg/gsp"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestDecode(t *testing.T) {
	conn, err := net.Dial("tcp", "localhost:38080")
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	codec := gsp.Codec{}
	data, err := codec.Decode(conn)
	log.Printf("chunkData: %s", string(data.Payload))
	arr := strings.Split(string(data.Payload), ",")
	if len(arr) == 2 {
		blockNum, _ := strconv.ParseInt(arr[0], 10, 64)
		fileSize, _ := strconv.ParseInt(arr[1], 10, 64)
		f, _ := os.Create("out.apk")
		f.Truncate(fileSize)
		ck := chunk.NewFileChunk(f)
		for i := int64(0); i < blockNum; i++ {
			d := codec.Encode(gsp.TypeJSON, []byte(strconv.FormatInt(i, 10)))
			_, _ = conn.Write(d)
			data, err := codec.Decode(conn)
			if err != nil {
				if err == io.EOF {
					log.Println("服务器断开连接")
				} else {
					log.Printf("读取协议出错: %v", err)
				}
				break
			}
			if data.Type == gsp.TypeFileChunk {
				ck.Save(i, data.Payload)
			}
		}
	}

}

func TestEncode(t *testing.T) {
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
	f, _ := os.Open("test.apk")
	ck := chunk.NewFileChunk(f)
	chunkNum := ck.GetChunkNum()
	codec := gsp.Codec{}

	// 发送块大小
	data := codec.Encode(gsp.TypeJSON, []byte(strconv.FormatInt(chunkNum, 10)+","+strconv.FormatInt(ck.FileStat.Size(), 10)))
	_, _ = conn.Write(data)
	// 进入等待块请求模式
	go func() {
		for {
			data, err := codec.Decode(conn)
			if err != nil {
				if err == io.EOF {
					log.Println("服务器断开连接")
				} else {
					log.Printf("读取协议出错: %v", err)
				}
				break
			}
			if data.Type == gsp.TypeJSON {
				index, _ := strconv.ParseInt(string(data.Payload), 10, 64)
				chunkData, _ := ck.ReadChunk(index)
				d := codec.Encode(gsp.TypeFileChunk, chunkData)
				_, _ = conn.Write(d)
			}
		}
	}()
	select {}

}
