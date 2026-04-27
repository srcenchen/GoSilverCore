package io_queue

import "sync"

// IO QUEUE 将多线程大块的IO操作拉成单线程

type IoQueue interface {
}

type ioQueue struct {
}

var (
	instance IoQueue
	once     sync.Once
)

func GetInstance() IoQueue {
	once.Do(func() {
		instance = ioQueue{}
	})
	return instance
}
