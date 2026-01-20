package wrapper

import (
	"errors"
	"net"
	"strings"
	"sync"
	"time"
)

// MigratableUDP 用于 restore 后重建 UDP socket。
// server 在收到 SIGUSR2 时调用 Rebind。

type MigratableUDP struct {
	mu sync.Mutex

	network string
	laddr   *net.UDPAddr
	conn    *net.UDPConn
	gen     uint64
}

func ListenMigratableUDP(network string, laddr *net.UDPAddr) (*MigratableUDP, error) {
	c, err := net.ListenUDP(network, laddr)
	if err != nil {
		return nil, err
	}
	return &MigratableUDP{network: network, laddr: laddr, conn: c, gen: 1}, nil
}

func (m *MigratableUDP) Rebind() error {
	// IMPORTANT: quic-go is concurrently calling ReadFrom on m.conn.
	// If we close the conn that a goroutine is blocked on, it unblocks with
	// "use of closed network connection" which may be treated as fatal by quic-go.
	// So we (1) create the new conn first, (2) swap, (3) close the old conn,
	// and (4) make ReadFrom/WriteTo retry when they observe a swap.

	newConn, err := net.ListenUDP(m.network, m.laddr)
	if err != nil {
		return err
	}

	m.mu.Lock()
	old := m.conn
	if old == nil {
		m.mu.Unlock()
		_ = newConn.Close()
		return errors.New("udp conn is nil")
	}
	m.conn = newConn
	m.gen++
	m.mu.Unlock()

	// Closing old may unblock an in-flight ReadFrom; it should retry on the new conn.
	_ = old.Close()
	return nil
}

func isNetClosing(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	// Fallback for some platforms/Go versions.
	return strings.Contains(err.Error(), "use of closed network connection")
}

func (m *MigratableUDP) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		m.mu.Lock()
		c := m.conn
		g := m.gen
		m.mu.Unlock()
		if c == nil {
			return 0, nil, errors.New("udp conn is nil")
		}

		n, addr, err := c.ReadFrom(p)
		if err == nil {
			return n, addr, nil
		}

		if isNetClosing(err) {
			m.mu.Lock()
			same := m.conn == c && m.gen == g
			m.mu.Unlock()
			if !same {
				// A Rebind swapped the conn while we were blocked. Retry with the new one.
				continue
			}
		}

		return 0, nil, err
	}
}

func (m *MigratableUDP) WriteTo(p []byte, addr net.Addr) (int, error) {
	for {
		m.mu.Lock()
		c := m.conn
		g := m.gen
		m.mu.Unlock()
		if c == nil {
			return 0, errors.New("udp conn is nil")
		}

		n, err := c.WriteTo(p, addr)
		if err == nil {
			return n, nil
		}
		if isNetClosing(err) {
			m.mu.Lock()
			same := m.conn == c && m.gen == g
			m.mu.Unlock()
			if !same {
				continue
			}
		}
		return n, err
	}
}

func (m *MigratableUDP) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil {
		return nil
	}
	err := m.conn.Close()
	m.conn = nil
	m.gen++
	return err
}

func (m *MigratableUDP) LocalAddr() net.Addr {
	m.mu.Lock()
	c := m.conn
	m.mu.Unlock()
	if c == nil {
		return &net.UDPAddr{}
	}
	return c.LocalAddr()
}

func (m *MigratableUDP) SetDeadline(t time.Time) error {
	m.mu.Lock()
	c := m.conn
	m.mu.Unlock()
	return c.SetDeadline(t)
}

func (m *MigratableUDP) SetReadDeadline(t time.Time) error {
	m.mu.Lock()
	c := m.conn
	m.mu.Unlock()
	return c.SetReadDeadline(t)
}

func (m *MigratableUDP) SetWriteDeadline(t time.Time) error {
	m.mu.Lock()
	c := m.conn
	m.mu.Unlock()
	return c.SetWriteDeadline(t)
}
