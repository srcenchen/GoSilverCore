package gsp_sdk

import (
	"encoding/json"
	"go-silver-core/internal/gsp_sdk/handle"
	"net"
)

type HandlerFunc func(conn net.Conn, data []byte, tool handle.ToolSession)

var Mux = map[string]HandlerFunc{
	"getChunk": handle.GetChunk,
}

func (s *Session) SenderOperation(conn net.Conn, payload []byte) error {
	var baseJson BaseJson
	err := json.Unmarshal(payload, &baseJson)
	if err != nil {
		return err
	}
	if handler, ok := Mux[baseJson.Operate]; ok {
		handler(conn, payload, s)
	}
	return nil
}
