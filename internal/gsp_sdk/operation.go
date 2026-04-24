package gsp_sdk

import (
	"encoding/json"
	"go-silver-core/internal/gsp_sdk/handle"
	"go-silver-core/internal/gsp_sdk/model"
	"net"
)

type HandlerFunc func(conn net.Conn, data []byte, tool handle.ToolSession)

var Mux = map[string]HandlerFunc{
	"getChunk":          handle.GetChunk,
	"getFileStatus":     handle.GetFileStatus,
	"wantChunk":         handle.WantChunk,
	"reportChunkStatus": handle.ReportChunkStatus,
}

func (s *Session) SenderOperation(conn net.Conn, payload []byte) error {
	var baseJson model.BaseJson
	err := json.Unmarshal(payload, &baseJson)
	if err != nil {
		return err
	}
	if handler, ok := Mux[baseJson.Operate]; ok {
		handler(conn, payload, s)
	}
	return nil
}
