package wrapper

import (
	"fmt"
	"sync"

	"github.com/quic-go/quic-go"
)

// controlLoop 在专用控制流 stream 上运行。
//
// 契约：
//   - 收到 migrate 消息后：(1) 只关闭一次 migrateSeen；(2) 发送 ACK。
//   - 透明模式下，这里不做 target 切换/重连。
//     底层网络变化由 UDP proxy 切后端 + server UDP rebind 完成。
//
// 参数：
//   - migrateOnce：保证即使多次收到 migrate，也只 close migrateSeen 一次。
//   - migrateSeen：作为“一次性信号”通知 APP 进入迁移态。
func (m *Manager) controlLoop(ctrl quic.Stream, migrateOnce *sync.Once, migrateSeen chan<- struct{}) {
	lr := NewLineReader(ctrl)
	for {
		msg, ok, err := lr.Next()
		if err != nil || !ok {
			return
		}
		if msg.Type != TypeMigrate {
			continue
		}
		newTarget := fmt.Sprintf("%s:%d", msg.NewAddr, msg.NewPort)
		fmt.Printf("[MIGRATION] migrate: id=%s new=%s\n", msg.ID, newTarget)
		tracef("migrate received id=%s new=%s", msg.ID, newTarget)
		if migrateOnce != nil {
			migrateOnce.Do(func() {
				close(migrateSeen)
			})
		}
		// 立即发送 ACK，便于 server/control 继续推进 CRIU dump/restore。
		// 注意：ACK 不代表“客户端业务已恢复”，只代表客户端在控制流上观测到了 migrate 事件。
		_ = WriteLine(ctrl, Message{Type: TypeAck, AckID: msg.ID})
	}
}
