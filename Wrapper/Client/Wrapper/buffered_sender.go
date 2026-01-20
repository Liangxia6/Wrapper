package wrapper

import (
	"bufio"
	"context"
	"time"

	"github.com/quic-go/quic-go"
)

// bufferedSender binds to one QUIC session and flushes Outbox.
// It is best-effort: on any write/read error it stops and lets Manager reconnect.
type bufferedSender struct {
	outbox *Outbox
	sendCh <-chan []byte

	quiet bool
}

func (s *bufferedSender) run(ctx context.Context, conn quic.Connection) error {
	st, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return err
	}
	defer st.Close()

	// Drain echoes so the peer won't block (echo server writes back).
	readErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			_, err := st.Read(buf)
			if err != nil {
				readErr <- err
				return
			}
		}
	}()

	w := bufio.NewWriterSize(st, 32*1024)

	flushBatch := func(items [][]byte) error {
		if len(items) == 0 {
			return nil
		}
		// Keep this tight; we want fast flush after reconnect.
		_ = st.SetWriteDeadline(time.Now().Add(300 * time.Millisecond))
		for _, b := range items {
			if len(b) == 0 {
				continue
			}
			if _, err := w.Write(b); err != nil {
				return err
			}
		}
		return w.Flush()
	}

	// Initial drain.
	for {
		select {
		case err := <-readErr:
			return err
		default:
		}
		items := s.outbox.Drain(512)
		if len(items) == 0 {
			break
		}
		if err := flushBatch(items); err != nil {
			// Push back unsent batch to the front is complex; best-effort: re-enqueue.
			for _, it := range items {
				_ = s.outbox.Enqueue(it)
			}
			return err
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			return err
		case b := <-s.sendCh:
			if len(b) == 0 {
				continue
			}
			// Try send immediately; if it fails, re-enqueue.
			_ = st.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
			if _, err := w.Write(b); err != nil {
				_ = s.outbox.Enqueue(b)
				return err
			}
			if err := w.Flush(); err != nil {
				_ = s.outbox.Enqueue(b)
				return err
			}
		}
	}
}

// Helper used by Manager.SendLine.
func encodeLine(s string) []byte {
	b := []byte(s)
	if len(b) == 0 {
		return []byte{'\n'}
	}
	if b[len(b)-1] != '\n' {
		b = append(b, '\n')
	}
	return b
}

func isCtxDone(ctx context.Context) bool { return ctx.Err() != nil }
