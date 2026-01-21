package wrapper

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"time"
)

// commitListener 监听一个“带外(Out-of-band)”的 commit 信号。
//
// 背景：
// - 我们现在的迁移是两阶段：
//  1. server(A) 在最终停止前通过 QUIC 控制流发 migrate(newAddr/newPort) 给 client（prepare）。
//     client 收到后只 ArmPeer，不立刻切换，从而在 pre-dump 窗口继续和 A 通信。
//  2. 当 B restore + UDP rebind 完成后，由宿主机上的 Control 进程向 client 发送 commit（commit）。
//     client 收到 commit 后立刻 CutoverToArmedPeer()，不再依赖业务 IO 超时触发。
//
// 注意：
// - 该监听器是“可选加速路径”。如果 commit 没收到，APP 仍可按原有策略：在 IO error 时 cutover。
// - 为简化实现，这里使用本机 UDP（默认 127.0.0.1:7360）。
func commitListener(ctx context.Context, listenAddr string, cutover func() bool) error {
	addr := strings.TrimSpace(listenAddr)
	if addr == "" {
		addr = strings.TrimSpace(os.Getenv("COMMIT_LISTEN_ADDR"))
	}
	if addr == "" {
		addr = "127.0.0.1:7360"
	}
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	defer pc.Close()

	buf := make([]byte, 2048)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_ = pc.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return err
		}

		payload := strings.TrimSpace(string(buf[:n]))
		if payload == "" {
			continue
		}

		// Support both plain text and JSON for convenience.
		// - plain: "commit"
		// - json : {"type":"commit", ...}
		if strings.EqualFold(payload, "commit") {
			if cutover != nil && cutover() {
				tracef("commit received (plain); cutover done")
			} else {
				tracef("commit received (plain); no cutover (no armed peer?)")
			}
			continue
		}

		var msg Message
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			tracef("commit listener: ignore payload=%q err=%v", payload, err)
			continue
		}
		if msg.Type != TypeCommit {
			continue
		}
		if cutover != nil && cutover() {
			tracef("commit received id=%s; cutover done", msg.ID)
		} else {
			tracef("commit received id=%s; no cutover (no armed peer?)", msg.ID)
		}
	}
}
