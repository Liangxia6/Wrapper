package wrapper

import (
	"context"
	"time"

	"github.com/quic-go/quic-go"
)

func dialControl(ctx context.Context, target string, clientID string, dialTimeout time.Duration) (quic.Connection, quic.Stream, error) {
	if dialTimeout <= 0 {
		dialTimeout = 900 * time.Millisecond
	}
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	// quic.Config controls transport-level behavior.
	//
	// KeepAlivePeriod:
	//   - Sends periodic keep-alives to reduce idle timeouts through NAT/firewalls.
	//
	// HandshakeIdleTimeout:
	//   - Upper bound for the handshake phase; we tie it to DialTimeout here.
	qc := &quic.Config{KeepAlivePeriod: 2 * time.Second, HandshakeIdleTimeout: dialTimeout}

	// Try 0-RTT first (quic.DialAddrEarly).
	//
	// quic-go semantics:
	//   - DialAddrEarly returns an EarlyConnection that allows sending application data
	//     before the handshake fully completes *if* the server accepts 0-RTT.
	//   - Whether 0-RTT was actually used is visible via ConnectionState().Used0RTT.
	//
	// This optimization mostly matters for reconnect-based flows. In transparent mode,
	// we still keep it because it is safe and helps if the session gets rebuilt.
	start := time.Now()
	sessEarly, errEarly := quic.DialAddrEarly(dialCtx, target, ClientTLSConfig(), qc)
	var sess quic.Connection
	usedEarly := false
	if errEarly == nil {
		sess = sessEarly
		usedEarly = true
	} else {
		sess, errEarly = quic.DialAddr(dialCtx, target, ClientTLSConfig(), qc)
		if errEarly != nil {
			return nil, nil, errEarly
		}
	}
	ctrl, err := sess.OpenStreamSync(dialCtx)
	if err != nil {
		_ = sess.CloseWithError(1, "open ctrl")
		return nil, nil, err
	}

	// First message on the control stream is a "hello" identifying the client.
	_ = WriteLine(ctrl, Message{Type: TypeHello, ClientID: clientID})
	st := sess.ConnectionState()
	tracef("dial ok target=%s early=%v used0rtt=%v dt=%dms", target, usedEarly, st.Used0RTT, time.Since(start).Milliseconds())
	return sess, ctrl, nil
}
