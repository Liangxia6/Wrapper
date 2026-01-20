package wrapper

import (
	"errors"
	"net"
	"strings"
	"sync"
	"time"
)

// SwappableUDPConn 是一个给 quic-go 使用的 net.PacketConn 包装。
//
// 目标：让 QUIC “看见”的 UDP 端点稳定，但 wrapper 能在底层切换真实的对端地址。
//
// 典型用法：
//   - 初次 dial 时：fakePeer=初始 target（quic-go 认为对端就是它）；realPeer=同一个地址。
//   - 迁移时：控制流收到 migrate(new ip:port)，调用 SetPeer(newPeer)。
//   - quic-go 仍然对同一个 fakePeer 工作，但所有 UDP 写入都会发往 realPeer。
//   - ReadFrom 也会把来源地址“伪装成 fakePeer”，避免 quic-go 因对端变化而做额外处理。
//
// 注意：这是一种“把对端变化隐藏在 QUIC 之下”的策略。
// 它要求 wrapper 能拿到新对端地址（例如来自 migrate 控制消息）。
//
// 并发：
//   - quic-go 会并发调用 ReadFrom/WriteTo。
//   - SetPeer 也可能并发发生。
//   - 若未来需要本地 IP 变化（客户端迁移到新网卡/IP），可用 RebindLocal()。
//
// 安全/语义：
//   - 我们只做地址层面的“转发/伪装”，不修改 QUIC 数据内容。
//   - 这不会绕过 QUIC 的加密/认证（握手/密钥仍由 quic-go 管理）。
//   - 但它会隐藏路径迁移信息，因此更像“强制固定路径视图”。
//
// 该实现仅依赖 net.PacketConn 的基础接口，不使用 quic-go 的 OOBCapablePacketConn。
// 如果后续需要 ECN/OOB，可再扩展。
//
// 参考：Server 侧的 MigratableUDP 解决的是“本地 UDP socket rebind”；
// 这里额外解决的是“对端地址变更但 QUIC 不感知”。

type SwappableUDPConn struct {
	mu      sync.Mutex
	network string
	laddr   *net.UDPAddr
	conn    *net.UDPConn
	gen     uint64

	peerMu    sync.RWMutex
	realPeer  *net.UDPAddr
	armedPeer *net.UDPAddr
	fakePeer  net.Addr
}

func NewSwappableUDPConn(network string, laddr *net.UDPAddr, realPeer *net.UDPAddr, fakePeer net.Addr) (*SwappableUDPConn, error) {
	c, err := net.ListenUDP(network, laddr)
	if err != nil {
		return nil, err
	}
	return &SwappableUDPConn{network: network, laddr: laddr, conn: c, gen: 1, realPeer: realPeer, fakePeer: fakePeer}, nil
}

// SetPeer 切换真实对端地址（线程安全）。
//
// 注意：如果你希望“迁移消息先到，但继续使用旧对端直到真的断联”，
// 请优先用 ArmPeer() + CutoverToArmedPeer()，而不是在收到 migrate 时立即 SetPeer。
func (s *SwappableUDPConn) SetPeer(peer *net.UDPAddr) {
	s.peerMu.Lock()
	s.realPeer = peer
	s.peerMu.Unlock()
}

// ArmPeer 设置“候选对端”。不会立刻影响 UDP 收发。
//
// 典型用法：控制流收到 migrate(new) 时 ArmPeer(new)。
// 然后当旧对端真的不可用（例如业务 IO 超时）时，再 CutoverToArmedPeer()。
func (s *SwappableUDPConn) ArmPeer(peer *net.UDPAddr) {
	s.peerMu.Lock()
	s.armedPeer = peer
	s.peerMu.Unlock()
}

// CutoverToArmedPeer 将真实对端切换到 armedPeer（若存在）。
// 返回值表示是否发生了切换。
func (s *SwappableUDPConn) CutoverToArmedPeer() bool {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	if s.armedPeer == nil {
		return false
	}
	// If already cutover to the same peer, treat as no-op.
	if s.realPeer != nil && udpAddrEqual(s.realPeer, s.armedPeer) {
		return false
	}
	s.realPeer = s.armedPeer
	return true
}

func (s *SwappableUDPConn) getPeer() *net.UDPAddr {
	s.peerMu.RLock()
	p := s.realPeer
	s.peerMu.RUnlock()
	return p
}

func (s *SwappableUDPConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		s.mu.Lock()
		c := s.conn
		g := s.gen
		s.mu.Unlock()
		if c == nil {
			return 0, nil, errors.New("udp conn is nil")
		}

		n, from, err := c.ReadFromUDP(p)
		if err == nil {
			peer := s.getPeer()
			// 只接收当前 realPeer 的包，避免误收其他来源（例如端口复用/噪音）。
			if peer != nil && from != nil {
				if !udpAddrEqual(peer, from) {
					continue
				}
			}
			if s.fakePeer != nil {
				return n, s.fakePeer, nil
			}
			return n, from, nil
		}

		if isNetClosing(err) {
			s.mu.Lock()
			same := s.conn == c && s.gen == g
			s.mu.Unlock()
			if !same {
				continue
			}
		}
		return 0, nil, err
	}
}

func (s *SwappableUDPConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	peer := s.getPeer()
	if peer == nil {
		return 0, errors.New("real peer is nil")
	}
	for {
		s.mu.Lock()
		c := s.conn
		g := s.gen
		s.mu.Unlock()
		if c == nil {
			return 0, errors.New("udp conn is nil")
		}
		n, err := c.WriteToUDP(p, peer)
		if err == nil {
			return n, nil
		}
		if isNetClosing(err) {
			s.mu.Lock()
			same := s.conn == c && s.gen == g
			s.mu.Unlock()
			if !same {
				continue
			}
		}
		return n, err
	}
}

// RebindLocal 用于客户端本地地址变化时重建 UDP socket（可选能力）。
// laddr 为空表示沿用创建时的 laddr。
func (s *SwappableUDPConn) RebindLocal(laddr *net.UDPAddr) error {
	if laddr == nil {
		laddr = s.laddr
	}
	newConn, err := net.ListenUDP(s.network, laddr)
	if err != nil {
		return err
	}

	s.mu.Lock()
	old := s.conn
	if old == nil {
		s.mu.Unlock()
		_ = newConn.Close()
		return errors.New("udp conn is nil")
	}
	s.conn = newConn
	s.laddr = laddr
	s.gen++
	s.mu.Unlock()

	_ = old.Close()
	return nil
}

func (s *SwappableUDPConn) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil
	}
	err := s.conn.Close()
	s.conn = nil
	s.gen++
	return err
}

func (s *SwappableUDPConn) LocalAddr() net.Addr {
	s.mu.Lock()
	c := s.conn
	s.mu.Unlock()
	if c == nil {
		return &net.UDPAddr{}
	}
	return c.LocalAddr()
}

func (s *SwappableUDPConn) SetDeadline(t time.Time) error {
	s.mu.Lock()
	c := s.conn
	s.mu.Unlock()
	return c.SetDeadline(t)
}

func (s *SwappableUDPConn) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	c := s.conn
	s.mu.Unlock()
	return c.SetReadDeadline(t)
}

func (s *SwappableUDPConn) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	c := s.conn
	s.mu.Unlock()
	return c.SetWriteDeadline(t)
}

func udpAddrEqual(a, b *net.UDPAddr) bool {
	if a == nil || b == nil {
		return false
	}
	if a.Port != b.Port {
		return false
	}
	// Normalize IPv4-in-IPv6 forms.
	ai := a.IP
	bi := b.IP
	if ai != nil {
		ai = ai.To16()
	}
	if bi != nil {
		bi = bi.To16()
	}
	return ai != nil && bi != nil && ai.Equal(bi)
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
