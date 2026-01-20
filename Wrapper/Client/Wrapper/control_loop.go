package wrapper

import (
	"fmt"
	"sync"

	"github.com/quic-go/quic-go"
)

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
		_ = WriteLine(ctrl, Message{Type: TypeAck, AckID: msg.ID})
	}
}
