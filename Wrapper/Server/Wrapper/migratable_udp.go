package wrapper

import (
	"errors"
	"net"
	"strings"
	"sync"
	"time"
)

// MigratableUDP 是一个支持“restore 后 rebind”的 UDP socket 包装。
//
// 为什么需要它？
//   - CRIU restore 到容器 B 后，被恢复的进程需要创建一个新的 UDP socket，
//     以匹配新的网络命名空间/端口映射。
//   - 同时 quic-go 会并发调用 ReadFrom/WriteTo。
//   - 如果在 ReadFrom 阻塞期间直接 Close 旧 socket，会出现 "use of closed network connection"。
//
// 策略：
//   1) 先创建新 UDPConn。
//   2) 原子地 swap m.conn（并增加 generation）。
//   3) 再关闭旧 conn。
//   4) ReadFrom/WriteTo 观察到 close 错误且 generation 已变化时，自动重试。

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
