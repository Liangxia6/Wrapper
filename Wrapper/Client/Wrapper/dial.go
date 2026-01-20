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

	qc := &quic.Config{KeepAlivePeriod: 2 * time.Second, HandshakeIdleTimeout: dialTimeout}
	// Try 0-RTT first. This will only succeed after we have a cached session ticket
	// from a previous connection to the same server instance.
	// On first connect, or if the cache doesn't match, it will fail and we fall back.
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
	_ = WriteLine(ctrl, Message{Type: TypeHello, ClientID: clientID})
	st := sess.ConnectionState()
	tracef("dial ok target=%s early=%v used0rtt=%v dt=%dms", target, usedEarly, st.Used0RTT, time.Since(start).Milliseconds())
	return sess, ctrl, nil
}
