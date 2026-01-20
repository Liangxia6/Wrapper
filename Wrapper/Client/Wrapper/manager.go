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
	Target   string
	Quiet    bool
	ClientID string

	DialBackoff time.Duration
	DialTimeout time.Duration
}

type Session struct {
	Conn   quic.Connection
	Target string

	// MigrateSeen will be closed once a migrate control message is observed on this session.
	// APP may use it to tighten IO deadlines and detect cutover earlier.
	MigrateSeen <-chan struct{}
}

// Run 负责 QUIC 控制流 + 重连编排。
// 数据流（ping/echo 或未来 AI 业务流）由 APP 通过 run 回调实现。
// 约定：当发生迁移时，Wrapper 会记录新目标，并在当前连接断开后切换。
// 这样可以避免在“迁移尚未完成（B 尚未 restore ready）”时过早中断业务流。
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

		// Transparent mode keeps a stable target (typically a UDP proxy). Any address
		// changes are handled underneath without switching targets here.
	}
}
