package server

import (
	"encoding/json"
	"fmt"
	"go-silver-core/internal/gsp"
	"go-silver-core/internal/gsp_sdk/model"
	"go-silver-core/internal/gsp_sdk/server/handle"
	"net"
)

type HandlerFunc func(conn net.Conn, data []byte, tool handle.ToolSession)

var Mux = map[string]HandlerFunc{
	"getChunk":      handle.GetChunk,
	"getFileStatus": handle.GetFileStatus,
	"wantChunk":     handle.WantChunk,
	"reportChunk":   handle.ReportChunk,
	"peerReg":       handle.PeerReg,
	"reportPeer":    handle.PeerReport,
}

func (s *Session) SenderOperation(conn net.Conn, payload []byte) error {
	var baseJson model.BaseJson
	err := json.Unmarshal(payload, &baseJson)
	if err != nil {
		return err
	}
	if baseJson.Operate == "getNodeStatus" {
		payload, _ := json.Marshal(s.NodeStatus())
		return (&gsp.Codec{}).EncodeTo(conn, gsp.TypeJSON, payload)
	}
	if baseJson.Operate == "control" {
		s.Control(conn, payload)
		return nil
	}
	if handler, ok := Mux[baseJson.Operate]; ok {
		handler(conn, payload, s)
	} else {
		return fmt.Errorf("unknown operation %q", baseJson.Operate)
	}
	return nil
}
