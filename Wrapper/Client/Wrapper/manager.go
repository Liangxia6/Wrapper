package wrapper

import (
	"context"
	"errors"
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

	// Transparent keeps the target stable and avoids reconnect-on-migrate behavior.
	// Intended for deployments where a UDP proxy / stable endpoint hides backend changes.
	Transparent bool

	DialBackoff time.Duration
	DialTimeout time.Duration

	// Outbox buffers outbound business messages across reconnects.
	// If nil, a default outbox will be created in non-transparent mode.
	// In transparent mode this is intentionally not auto-created.
	Outbox *Outbox

	sendCh chan []byte

	activeMu   sync.Mutex
	activeSess quic.Connection
	activeCtx  context.Context
	activeStop context.CancelFunc

	mu             sync.Mutex
	prefetchCancel context.CancelFunc
	prefetchTarget string
	prefetchConn   quic.Connection
	prefetchCtrl   quic.Stream
}

var ErrSendUnsupportedInTransparent = errors.New("SendBytes/SendLine is unsupported in transparent mode; write on the data stream directly")

type Session struct {
	Conn   quic.Connection
	Target string

	// MigrateSeen will be closed once a migrate control message is observed on this session.
	// APP may use it to tighten IO deadlines and detect cutover earlier.
	MigrateSeen <-chan struct{}
}

func (m *Manager) startPrefetch(target string) {
	m.mu.Lock()
	if target == "" {
		m.mu.Unlock()
		return
	}
	if m.prefetchTarget == target && m.prefetchConn != nil {
		m.mu.Unlock()
		return
	}
	if m.prefetchCancel != nil {
		m.prefetchCancel()
		m.prefetchCancel = nil
	}
	// Close any stale prefetched conn.
	if m.prefetchConn != nil {
		_ = m.prefetchConn.CloseWithError(0, "stale prefetch")
		m.prefetchConn = nil
		m.prefetchCtrl = nil
		m.prefetchTarget = ""
	}

	pctx, cancel := context.WithCancel(context.Background())
	m.prefetchCancel = cancel
	m.prefetchTarget = target
	dialTimeout := m.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = 900 * time.Millisecond
	}
	clientID := m.ClientID
	quiet := m.Quiet
	m.mu.Unlock()

	go func() {
		// Aggressive retry to catch the moment B becomes ready.
		backoff := 10 * time.Millisecond
		if backoff < m.DialBackoff {
			// keep it small regardless of normal backoff
			backoff = 10 * time.Millisecond
		}
		tracef("prefetch start target=%s dialTimeout=%dms", target, dialTimeout.Milliseconds())
		for {
			if pctx.Err() != nil {
				return
			}
			conn, ctrl, err := dialControl(pctx, target, clientID, dialTimeout)
			if err == nil {
				st := conn.ConnectionState()
				tracef("prefetch ready target=%s used0rtt=%v", target, st.Used0RTT)
				m.mu.Lock()
				// Only publish if we are still prefetching the same target.
				if m.prefetchTarget == target {
					m.prefetchConn = conn
					m.prefetchCtrl = ctrl
					if !quiet {
						fmt.Printf("[PREFETCH] 新目标已就绪 %s\n", target)
					}
					m.mu.Unlock()
					return
				}
				m.mu.Unlock()
				_ = conn.CloseWithError(0, "prefetch stale")
				continue
			}
			time.Sleep(backoff)
		}
	}()
}

func (m *Manager) takePrefetch(target string) (quic.Connection, quic.Stream, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.prefetchConn == nil || m.prefetchTarget != target {
		return nil, nil, false
	}
	conn := m.prefetchConn
	ctrl := m.prefetchCtrl
	st := conn.ConnectionState()
	tracef("prefetch taken target=%s used0rtt=%v", target, st.Used0RTT)
	m.prefetchConn = nil
	m.prefetchCtrl = nil
	m.prefetchTarget = ""
	if m.prefetchCancel != nil {
		m.prefetchCancel()
		m.prefetchCancel = nil
	}
	return conn, ctrl, true
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
	if !m.Transparent {
		if m.Outbox == nil {
			m.Outbox = NewOutbox(OutboxOptions{DropOldest: true})
		}
		if m.sendCh == nil {
			m.sendCh = make(chan []byte, 8192)
		}
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		sess, ctrl, ok := m.takePrefetch(m.Target)
		var err error
		if !ok {
			sess, ctrl, err = dialControl(ctx, m.Target, m.ClientID, m.DialTimeout)
		}
		if err != nil {
			if !m.Quiet {
				fmt.Fprintf(os.Stderr, "[客户端] 连接失败：%v\n", err)
			}
			time.Sleep(m.DialBackoff)
			continue
		}

		fmt.Printf("✅ [Client] Connected %s\n", m.Target)
		tracef("session connected target=%s", m.Target)

		// Bind a buffered sender to this session (legacy reconnect mode only).
		if !m.Transparent {
			m.bindActiveSender(sess)
		}

		reconnect := make(chan string, 1)
		migrateSeen := make(chan struct{})
		var migrateOnce sync.Once
		ctrlDone := make(chan struct{})
		go func() {
			defer close(ctrlDone)
			m.controlLoop(ctrl, reconnect, &migrateOnce, migrateSeen)
		}()

		_ = run(ctx, &Session{Conn: sess, Target: m.Target, MigrateSeen: migrateSeen})
		tracef("session run ended target=%s", m.Target)
		if !m.Transparent {
			m.unbindActiveSender()
		}
		tracef("session closing target=%s", m.Target)
		_ = sess.CloseWithError(0, "session end")
		<-ctrlDone
		tracef("session ctrl loop done target=%s", m.Target)

		select {
		case nt := <-reconnect:
			if nt != "" {
				tracef("target switch old=%s new=%s", m.Target, nt)
				m.Target = nt
			}
		default:
		}
	}
}

func (m *Manager) bindActiveSender(conn quic.Connection) {
	m.activeMu.Lock()
	defer m.activeMu.Unlock()
	if m.activeStop != nil {
		m.activeStop()
		m.activeStop = nil
	}
	actx, cancel := context.WithCancel(context.Background())
	m.activeCtx = actx
	m.activeStop = cancel
	m.activeSess = conn

	s := &bufferedSender{outbox: m.Outbox, sendCh: m.sendCh, quiet: m.Quiet}
	go func() {
		_ = s.run(actx, conn)
	}()
}

func (m *Manager) unbindActiveSender() {
	m.activeMu.Lock()
	if m.activeStop != nil {
		m.activeStop()
		m.activeStop = nil
	}
	m.activeSess = nil
	m.activeCtx = nil
	m.activeMu.Unlock()
}

// SendLine buffers a text line and attempts to send it immediately if connected.
// It never blocks the caller for network IO.
func (m *Manager) SendLine(line string) error {
	return m.SendBytes(encodeLine(line))
}

// SendBytes buffers raw bytes (recommended to include trailing '\n' for line protocols).
// It never blocks the caller for network IO.
func (m *Manager) SendBytes(b []byte) error {
	if m.Transparent {
		return ErrSendUnsupportedInTransparent
	}
	if m.Outbox == nil {
		m.Outbox = NewOutbox(OutboxOptions{DropOldest: true})
	}
	if err := m.Outbox.Enqueue(b); err != nil {
		return err
	}
	if m.sendCh == nil {
		m.sendCh = make(chan []byte, 8192)
	}
	select {
	case m.sendCh <- b:
	default:
		// Sender is busy; message is already in outbox.
	}
	return nil
}
