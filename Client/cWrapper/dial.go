package wrapper

import (
	"context"
	"net"
	"time"

	"github.com/quic-go/quic-go"
)

func dialControl(ctx context.Context, target string, clientID string, dialTimeout time.Duration) (quic.Connection, quic.Stream, *SwappableUDPConn, error) {
	if dialTimeout <= 0 {
		dialTimeout = 900 * time.Millisecond
	}
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	// target 是“真实对端”（初始 dial 目标）。
	// 透明迁移时，target 可能在 migrate 消息中变化；但我们不会重建 QUIC，
	// 只需要把 SwappableUDPConn 的 realPeer 改掉。
	realPeer, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		return nil, nil, nil, err
	}
	// fakePeer 是 quic-go 看到的“逻辑对端”。
	// 我们让它保持不变（初始等于 target），并在 ReadFrom 里伪装来源地址。
	// 这样 QUIC 层就不会因为对端 IP/端口变化而感知到“路径变更”。
	fakePeer := realPeer
	pc, err := NewSwappableUDPConn("udp", nil, realPeer, fakePeer)
	if err != nil {
		return nil, nil, nil, err
	}

	// quic.Config 用于控制 QUIC 传输层行为。
	//
	// KeepAlivePeriod：
	//   - 定期发送 keep-alive，减少 NAT/防火墙导致的空闲超时。
	//
	// HandshakeIdleTimeout：
	//   - 握手阶段的超时上限；这里直接绑定到 DialTimeout。
	qc := &quic.Config{KeepAlivePeriod: 2 * time.Second, HandshakeIdleTimeout: dialTimeout}

	// 优先尝试 0-RTT（quic.DialAddrEarly）。
	//
	// quic-go 的语义（只描述接口层面）：
	//   - DialAddrEarly 返回 EarlyConnection，如果服务端接受 0-RTT，则可以在握手完全完成前发送应用数据。
	//   - 是否真正使用了 0-RTT，可以通过 ConnectionState().Used0RTT 判断。
	//
	// 这个优化在“重连式迁移”里收益更大；透明模式下我们也保留它，
	// 因为它是安全的，并且当 session 真的需要重建时仍能降低延迟。
	start := time.Now()
	sessEarly, errEarly := quic.DialEarly(dialCtx, pc, fakePeer, ClientTLSConfig(), qc)
	var sess quic.Connection
	usedEarly := false
	if errEarly == nil {
		sess = sessEarly
		usedEarly = true
	} else {
		sess, errEarly = quic.Dial(dialCtx, pc, fakePeer, ClientTLSConfig(), qc)
		if errEarly != nil {
			_ = pc.Close()
			return nil, nil, nil, errEarly
		}
	}
	ctrl, err := sess.OpenStreamSync(dialCtx)
	if err != nil {
		_ = sess.CloseWithError(1, "open ctrl")
		_ = pc.Close()
		return nil, nil, nil, err
	}

	// 控制流第一条消息："hello"，用于标识 client。
	_ = WriteLine(ctrl, Message{Type: TypeHello, ClientID: clientID})
	st := sess.ConnectionState()
	tracef("dial ok target=%s early=%v used0rtt=%v dt=%dms", target, usedEarly, st.Used0RTT, time.Since(start).Milliseconds())
	return sess, ctrl, pc, nil
}
