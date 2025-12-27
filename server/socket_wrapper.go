package main

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// MigratablePacketConn 是一个自定义的 PacketConn 实现
// 它封装了底层的 UDP Socket，提供了“暂停”和“恢复”功能，
// 以欺骗上层 QUIC 协议栈，使其在底层 Socket 关闭（为了 CRIU Checkpoint）时
// 不会报错断开，而是进入等待状态。
type MigratablePacketConn struct {
	mu          sync.RWMutex
	rawConn     net.PacketConn // 底层的真实 UDP 连接
	isMigrating bool           // 标志位：是否正在进行迁移
	cond        *sync.Cond     // 条件变量：用于阻塞和唤醒 ReadFrom 协程
}

// NewMigratablePacketConn 创建一个新的可迁移连接
func NewMigratablePacketConn(bindAddr string) (*MigratablePacketConn, error) {
	conn, err := net.ListenPacket("udp4", bindAddr)
	if err != nil {
		return nil, err
	}
	w := &MigratablePacketConn{
		rawConn: conn,
	}
	w.cond = sync.NewCond(&w.mu)
	return w, nil
}

// ReadFrom 重写了读取逻辑
// 这是实现“无感”的关键：
// 1. 正常状态：直接调用底层 Socket 读取。
// 2. 迁移状态：不返回错误，而是死循环等待 (cond.Wait())。
//    这让 QUIC 觉得网络只是卡住了，而不是连接断了。
func (w *MigratablePacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	w.mu.RLock()
	// 如果正在迁移，就在这里死等，绝不向上传递错误
	for w.isMigrating {
		w.cond.Wait() // 挂起当前协程，直到 Rebind 中调用 Broadcast
	}
	conn := w.rawConn
	w.mu.RUnlock()

	// 防御性代码：如果被唤醒但 conn 还是 nil，稍作等待返回空
	if conn == nil {
		time.Sleep(100 * time.Millisecond)
		return 0, nil, nil
	}
	return conn.ReadFrom(p)
}

// WriteTo 重写了写入逻辑
// 1. 正常状态：直接发送。
// 2. 迁移状态：直接丢弃数据包，但返回“成功”。
//    QUIC 协议有重传机制，这些丢弃的包稍后会被自动重传。
//    如果在这里阻塞 WriteTo，可能会导致上层逻辑卡死，所以丢包是更好的选择。
func (w *MigratablePacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	w.mu.RLock()
	// 发送端如果在迁移，可以选择直接丢包，QUIC 会重传
	if w.isMigrating || w.rawConn == nil {
		w.mu.RUnlock()
		return len(p), nil // 假装发送成功
	}
	conn := w.rawConn
	w.mu.RUnlock()
	return conn.WriteTo(p, addr)
}

// Close 关闭连接
func (w *MigratablePacketConn) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.rawConn != nil {
		return w.rawConn.Close()
	}
	return nil
}

// LocalAddr 获取本地地址
func (w *MigratablePacketConn) LocalAddr() net.Addr {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.rawConn != nil {
		return w.rawConn.LocalAddr()
	}
	// 如果 Socket 已关闭，返回一个空的 UDP 地址，防止上层空指针引用
	return &net.UDPAddr{IP: net.IPv4zero, Port: 0}
}

// 必须实现的接口方法，这里留空即可
func (w *MigratablePacketConn) SetDeadline(t time.Time) error      { return nil }
func (w *MigratablePacketConn) SetReadDeadline(t time.Time) error  { return nil }
func (w *MigratablePacketConn) SetWriteDeadline(t time.Time) error { return nil }

// PrepareForCheckpoint: 闪断开始 (响应 SIGUSR1)
// 这个方法在 CRIU Checkpoint 之前被调用。
// 它的任务是彻底关闭底层 Socket，清除所有打开的文件描述符，
// 从而满足 CRIU 的快照要求。
func (w *MigratablePacketConn) PrepareForCheckpoint() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.isMigrating = true // 开启迁移模式，ReadFrom 开始阻塞，WriteTo 开始丢包

	if w.rawConn != nil {
		w.rawConn.Close() // 真正关闭 Socket
		w.rawConn = nil
	}
	fmt.Println("🛑 [Wrapper] Socket 已安全关闭，ReadFrom 已进入阻塞模式")
}

// Rebind: 闪断结束 (响应 SIGUSR2)
// 这个方法在 CRIU Restore 之后被调用。
// 它的任务是重新建立网络连接，并唤醒被阻塞的读协程。
func (w *MigratablePacketConn) Rebind(newBindAddr string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// 重新监听端口
	newConn, err := net.ListenPacket("udp4", newBindAddr)
	if err != nil {
		return err
	}
	w.rawConn = newConn
	w.isMigrating = false // 关闭迁移模式

	w.cond.Broadcast() // 唤醒所有卡在 ReadFrom 里的协程
	fmt.Println("♻️ [Wrapper] Socket 已重建，ReadFrom 已恢复运行")
	return nil
}
