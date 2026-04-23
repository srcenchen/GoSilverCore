package gsp_sdk

import (
	"encoding/json"
	"net"
)

type HandlerFunc func(conn net.Conn, data []byte)

var Mux = map[string]HandlerFunc{}

func SenderOperation(conn net.Conn, payload []byte) error {
	var baseJson BaseJson
	err := json.Unmarshal(payload, &baseJson)
	if err != nil {
		return err
	}
	if handler, ok := Mux[baseJson.Operate]; ok {
		handler(conn, payload)
	}
	return nil
}
