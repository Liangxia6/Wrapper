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
	// Target 是我们首次 dial 的地址（真实对端）。
	//
	// 在“QUIC 透明迁移（内部 UDP 解耦）”模式下：
	//   - QUIC 会绑定到一个稳定的 net.PacketConn（SwappableUDPConn）。
	//   - 迁移时不重建 QUIC session，而是把 SwappableUDPConn 的 real peer 切到新地址。
	//   - 因此 Target 仅用于初始连接，后续对端变化由 migrate 控制消息驱动。
	Target   string
	// Quiet 用于减少用户侧日志（TRACE 仍由环境变量 TRACE=1 控制）。
	Quiet    bool
	// ClientID 会在初始 "hello" 控制消息中发送。
	// 主要用于服务端/控制端的调试和身份区分。
	ClientID string

	// DialBackoff 是连接失败后的重试间隔。
	DialBackoff time.Duration
	// DialTimeout 限制一次 dial 尝试的最长时间（包含握手）。
	DialTimeout time.Duration
}

type Session struct {
	// Conn 是当前活跃的 quic-go 连接（一个 QUIC session）。
	Conn   quic.Connection
	// Target 是从 Manager.Target 复制来的便捷字段。
	Target string

	// MigrateSeen：当控制流观测到 migrate 消息后会 close 一次。
	// APP 可以用它在迁移期收紧 IO deadline，从而更快进入“故障判定/恢复”逻辑。
	MigrateSeen <-chan struct{}
}

// Run 是客户端 wrapper 的主循环。
//
// 结构：
//   1) dial 到 Manager.Target 建立 QUIC 连接。
//   2) 打开控制流 stream，并在 goroutine 中运行 controlLoop。
//   3) 调用 APP 回调；业务 stream 与 IO 由 APP 自己管理。
//   4) 回调返回后关闭 session；若 ctx 未取消则重试。
//
// 透明迁移契约：
//   - wrapper 在 migrate 发生时不切 target。
//   - controlLoop 只负责触发 MigrateSeen + 发送 ACK。
//   - A->B 的变化在 QUIC 之下完成（UDP proxy 切后端 + server UDP rebind）。
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

		sess, ctrl, pc, err := dialControl(ctx, m.Target, m.ClientID, m.DialTimeout)
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
			m.controlLoop(ctrl, pc, &migrateOnce, migrateSeen)
		}()

		_ = run(ctx, &Session{Conn: sess, Target: m.Target, MigrateSeen: migrateSeen})
		tracef("session run ended target=%s", m.Target)
		tracef("session closing target=%s", m.Target)
		_ = sess.CloseWithError(0, "session end")
		<-ctrlDone
		tracef("session ctrl loop done target=%s", m.Target)

		// 透明迁移模式下：这里不会切 target，也不会重建 QUIC。
		// 若连接最终结束，则从初始 Target 重新 dial。
	}
}
