package gsp_sdk

// 定义相关JSON结构

// BaseJson 最基本的JSON，用于解析出 Operate
type BaseJson struct {
	Operate string `json:"operate"` // 操作类型
}
