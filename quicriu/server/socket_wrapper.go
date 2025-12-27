package main

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// MigratablePacketConn 封装 UDP Socket，支持“闪断”和“重建”
type MigratablePacketConn struct {
	mu          sync.RWMutex
	rawConn     net.PacketConn
	isMigrating bool
	cond        *sync.Cond
}

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

// ReadFrom: 迁移期间阻塞，不报错
func (w *MigratablePacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	for {
		w.mu.Lock()
		for w.isMigrating {
			w.cond.Wait()
		}
		conn := w.rawConn
		w.mu.Unlock()

		if conn == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		n, addr, err = conn.ReadFrom(p)
		if err != nil {
			w.mu.RLock()
			isMigrating := w.isMigrating
			w.mu.RUnlock()

			// 如果是因为迁移导致的关闭，则忽略错误，重新进入等待循环
			if isMigrating {
				continue
			}
		}
		return n, addr, err
	}
}

// WriteTo: 迁移期间丢包，假装成功
func (w *MigratablePacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	w.mu.RLock()
	if w.isMigrating || w.rawConn == nil {
		w.mu.RUnlock()
		return len(p), nil
	}
	conn := w.rawConn
	w.mu.RUnlock()
	return conn.WriteTo(p, addr)
}

func (w *MigratablePacketConn) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.rawConn != nil {
		return w.rawConn.Close()
	}
	return nil
}

func (w *MigratablePacketConn) LocalAddr() net.Addr {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.rawConn != nil {
		return w.rawConn.LocalAddr()
	}
	return &net.UDPAddr{IP: net.IPv4zero, Port: 0}
}

func (w *MigratablePacketConn) SetDeadline(t time.Time) error      { return nil }
func (w *MigratablePacketConn) SetReadDeadline(t time.Time) error  { return nil }
func (w *MigratablePacketConn) SetWriteDeadline(t time.Time) error { return nil }

// PrepareForCheckpoint: 关闭 Socket，进入阻塞模式 (Pre-dump 开始)
func (w *MigratablePacketConn) PrepareForCheckpoint() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.isMigrating = true
	if w.rawConn != nil {
		w.rawConn.Close()
		w.rawConn = nil
	}
	fmt.Println("🛑 [Wrapper] Socket 已关闭 (Pre-dump Mode)")
}

// Rebind: 重建 Socket，恢复通信 (Pre-dump 结束)
func (w *MigratablePacketConn) Rebind(newBindAddr string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	newConn, err := net.ListenPacket("udp4", newBindAddr)
	if err != nil {
		return err
	}
	w.rawConn = newConn
	w.isMigrating = false
	w.cond.Broadcast()
	fmt.Println("♻️ [Wrapper] Socket 已重建 (Service Resumed)")
	return nil
}
