package wrapper

import (
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

// ControlClient 封装服务端的控制流：
// - 读取 client -> server 的 ack
// - server -> client 发送 migrate 并等待 ack
//
// 业务数据流（AI 应用的数据）不在这里处理。

type ControlClient struct {
	ctrl quic.Stream

	ackMu  sync.Mutex
	ackMap map[string]chan struct{}

	done chan struct{}
}

func NewControlClient(ctrl quic.Stream) *ControlClient {
	return &ControlClient{
		ctrl:   ctrl,
		ackMap: map[string]chan struct{}{},
		done:   make(chan struct{}),
	}
}

func (c *ControlClient) Start() {
	go func() {
		defer close(c.done)
		lr := NewLineReader(c.ctrl)
		for {
			msg, ok, err := lr.Next()
			if err != nil || !ok {
				return
			}
			if msg.Type != TypeAck {
				continue
			}
			c.ackMu.Lock()
			ch := c.ackMap[msg.AckID]
			c.ackMu.Unlock()
			if ch != nil {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		}
	}()
}

func (c *ControlClient) Done() <-chan struct{} { return c.done }

func (c *ControlClient) SendMigrateAndWait(id, newAddr string, newPort int, timeout time.Duration) (wait time.Duration, acked bool) {
	start := time.Now()

	c.ackMu.Lock()
	ch := make(chan struct{}, 1)
	c.ackMap[id] = ch
	c.ackMu.Unlock()

	_ = WriteLine(c.ctrl, Message{Type: TypeMigrate, ID: id, NewAddr: newAddr, NewPort: newPort})

	select {
	case <-ch:
		acked = true
	case <-time.After(timeout):
		acked = false
	case <-c.done:
		acked = false
	}

	c.ackMu.Lock()
	delete(c.ackMap, id)
	c.ackMu.Unlock()

	wait = time.Since(start)
	return wait, acked
}
