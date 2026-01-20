package wrapper

import (
	"fmt"
	"net"
	"sync"

	"github.com/quic-go/quic-go"
)

// controlLoop 在专用控制流 stream 上运行。
//
// 契约：
//   - 收到 migrate 消息后：(1) 只关闭一次 migrateSeen；(2) 发送 ACK。
//   - 透明模式下，这里不做 target 切换/重连。
//     我们只更新底层 UDP 的真实对端地址（SwappableUDPConn.SetPeer），让 QUIC 不感知变化。
//
// 参数：
//   - migrateOnce：保证即使多次收到 migrate，也只 close migrateSeen 一次。
//   - migrateSeen：作为“一次性信号”通知 APP 进入迁移态。
func (m *Manager) controlLoop(ctrl quic.Stream, pc *SwappableUDPConn, migrateOnce *sync.Once, migrateSeen chan<- struct{}) {
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

		// 核心：不重建 QUIC，而是切换底层 UDP 的真实对端。
		if pc != nil {
			if na, rerr := net.ResolveUDPAddr("udp", newTarget); rerr == nil {
				pc.SetPeer(na)
				tracef("udp peer switched to=%s", na.String())
			} else {
				tracef("udp peer switch failed target=%s err=%v", newTarget, rerr)
			}
		}
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
