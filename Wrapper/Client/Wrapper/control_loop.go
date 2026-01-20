package wrapper

import (
	"fmt"
	"sync"

	"github.com/quic-go/quic-go"
)

func (m *Manager) controlLoop(ctrl quic.Stream, reconnect chan<- string, migrateOnce *sync.Once, migrateSeen chan<- struct{}) {
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
		_ = WriteLine(ctrl, Message{Type: TypeAck, AckID: msg.ID})
		if m.Transparent {
			// In transparent mode we keep a stable target (e.g. UDP proxy) and let the
			// underlying network switch happen without rebuilding QUIC sessions.
			continue
		}
		// Start background prefetch ASAP; switchover still happens after the current session ends.
		m.startPrefetch(newTarget)
		fmt.Printf("[RECONNECT] 已记录新目标 %s（后台预连接中）\n", newTarget)
		select {
		case reconnect <- newTarget:
		default:
		}
	}
}
