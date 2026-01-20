package wrapper

import (
	"fmt"
	"sync"

	"github.com/quic-go/quic-go"
)

// controlLoop runs on the dedicated control stream.
//
// Contract:
//   - When we receive a migrate message, we (1) close migrateSeen exactly once and
//     (2) send an ACK.
//   - In transparent mode we do NOT change targets / reconnect here.
//     The underlying network change is handled by the UDP proxy + server UDP rebind.
//
// Parameters:
//   - migrateOnce ensures migrateSeen closes once even if multiple migrate messages arrive.
//   - migrateSeen is a channel used as a one-shot signal to the application.
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
		// ACK is sent immediately so the server/control layer can proceed with CRIU dump/restore.
		// It does not imply that the client has "recovered"; it only means the client observed
		// the migrate event on the control stream.
		_ = WriteLine(ctrl, Message{Type: TypeAck, AckID: msg.ID})
	}
}
