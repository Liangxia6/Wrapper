package wrapper

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

type Manager struct {
	// Target is the stable remote address we dial.
	//
	// In transparent mode, Target typically points to the UDP proxy (e.g. 127.0.0.1:5342).
	// The proxy then forwards UDP to the current backend server (A or B).
	Target   string
	// Quiet reduces user-facing logs (TRACE remains controlled by TRACE=1).
	Quiet    bool
	// ClientID is sent in the initial "hello" control message.
	// It can be used by the server/control layer for debugging or identification.
	ClientID string

	// DialBackoff is the retry delay between failed connection attempts.
	DialBackoff time.Duration
	// DialTimeout bounds a single dial attempt (including handshake).
	DialTimeout time.Duration
}

type Session struct {
	// Conn is the active quic-go connection (a QUIC session).
	Conn   quic.Connection
	// Target is copied from Manager.Target for convenience.
	Target string

	// MigrateSeen will be closed once a migrate control message is observed on this session.
	// APP may use it to tighten IO deadlines and detect cutover earlier.
	MigrateSeen <-chan struct{}
}

// Run is the main event loop of the client wrapper.
//
// Structure:
//   1) Dial QUIC to Manager.Target.
//   2) Open a control stream and start controlLoop in a goroutine.
//   3) Invoke the application callback, which owns business streams and IO.
//   4) When the callback returns, close the session and (unless ctx canceled) retry.
//
// Transparent migration contract:
//   - The wrapper DOES NOT switch targets on migrate.
//   - controlLoop only signals MigrateSeen and sends ACK.
//   - Any A->B change is handled under the QUIC layer (UDP proxy + server UDP rebind).
func (m *Manager) Run(ctx context.Context, run func(ctx context.Context, s *Session) error) error {
	if m.Target == "" {
		m.Target = "127.0.0.1:5242"
	}
	if m.ClientID == "" {
		m.ClientID = "car"
	}
	if m.DialBackoff <= 0 {
		m.DialBackoff = 50 * time.Millisecond
	}
	if m.DialTimeout <= 0 {
		m.DialTimeout = 900 * time.Millisecond
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		sess, ctrl, err := dialControl(ctx, m.Target, m.ClientID, m.DialTimeout)
		if err != nil {
			if !m.Quiet {
				fmt.Fprintf(os.Stderr, "[客户端] 连接失败：%v\n", err)
			}
			time.Sleep(m.DialBackoff)
			continue
		}

		fmt.Printf("✅ [Client] Connected %s\n", m.Target)
		tracef("session connected target=%s", m.Target)

		migrateSeen := make(chan struct{})
		var migrateOnce sync.Once
		ctrlDone := make(chan struct{})
		go func() {
			defer close(ctrlDone)
			m.controlLoop(ctrl, &migrateOnce, migrateSeen)
		}()

		_ = run(ctx, &Session{Conn: sess, Target: m.Target, MigrateSeen: migrateSeen})
		tracef("session run ended target=%s", m.Target)
		tracef("session closing target=%s", m.Target)
		_ = sess.CloseWithError(0, "session end")
		<-ctrlDone
		tracef("session ctrl loop done target=%s", m.Target)

		// In transparent mode we always retry the same Target.
		// Backend changes are handled by the proxy, not by this loop.
	}
}
