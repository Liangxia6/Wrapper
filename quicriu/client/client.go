package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
)

type MigrationCmd struct {
	Cmd     string `json:"cmd"`
	NewIP   string `json:"new_ip"`
	NewPort int    `json:"new_port"`
}

var sessionCache = tls.NewLRUClientSessionCache(100)

func main() {
	tlsConf := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"mec-migration"},
		ClientSessionCache: sessionCache,
	}

	target := "127.0.0.1:4242" // 初始连接的是容器映射端口
	var seq int64 = 0
	var migrationStartTime time.Time

	for {
		newTarget, reconnect := connectAndLoop(target, tlsConf, migrationStartTime, &seq)
		if !reconnect {
			break
		}
		target = newTarget
		migrationStartTime = time.Now()
		fmt.Printf("🚀 [RECONNECT] 开始连接新目标: %s\n", target)
	}
}

func connectAndLoop(addr string, tlsConf *tls.Config, migrationStartTime time.Time, seq *int64) (string, bool) {
	fmt.Printf("🚗 Connecting to %s...\n", addr)

	var session quic.Connection
	var err error

	// 重试循环：尝试连接直到成功或达到最大次数
	for i := 0; i < 200; i++ { // 增加重试次数，因为 Restore 可能需要几秒
		// 0-RTT 尝试 (短超时)
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		session, err = quic.DialAddrEarly(ctx, addr, tlsConf, nil)
		cancel()

		if err == nil {
			break
		}

		// 普通连接尝试
		ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
		session, err = quic.DialAddr(ctx, addr, tlsConf, nil)
		cancel()

		if err == nil {
			break
		}

		// fmt.Printf("⏳ Connection failed, retrying... (%v)\n", err)
		time.Sleep(50 * time.Millisecond) // 更快的重试频率
	}

	if err != nil {
		fmt.Println("❌ Failed to connect after retries:", err)
		return "", false
	}

	stream, err := session.OpenStreamSync(context.Background())
	if err != nil {
		fmt.Println("❌ OpenStreamSync failed:", err)
		return "", false
	}

	if !migrationStartTime.IsZero() {
		duration := time.Since(migrationStartTime)
		fmt.Printf("✅ Reconnected in %dms\n", duration.Milliseconds())
	} else {
		fmt.Println("✅ Connected! Sending data...")
	}

	// 使用 Context 控制发送循环的退出
	sendCtx, sendCancel := context.WithCancel(context.Background())
	defer sendCancel()

	migrationChan := make(chan string, 1)

	// 启动接收协程 (监听迁移指令)
	go func() {
		defer sendCancel() // 退出时取消 Context，停止发送循环
		buf := make([]byte, 1024)
		for {
			n, err := stream.Read(buf)
			if err != nil {
				fmt.Println("❌ Connection closed:", err)
				return
			}
			
			// 检查是否是迁移指令
			var cmd MigrationCmd
			if json.Unmarshal(buf[:n], &cmd) == nil && cmd.Cmd == "migrate" {
				fmt.Println("------------------------------------------------")
				fmt.Printf("📩 [MIGRATION] 收到迁移指令!\n")
				
				targetAddr := fmt.Sprintf("127.0.0.1:%d", cmd.NewPort)
				fmt.Println("🔄 [RECONNECT] 准备断开旧连接，发起新连接...")
				migrationChan <- targetAddr
				return
			}
			
			// 正常回显
			if len(buf[:n]) > 0 && buf[0] == 'P' { // 简单过滤
				// 只有当发送日志也打印时才打印回显，或者每10个打印一次
				// 这里为了简单，直接打印，但加上时间戳
				// fmt.Printf("📥 Echo: %s\n", buf[:n])
			}
			// 只有 Ping-X0 的时候打印
			str := string(buf[:n])
			if len(str) > 5 && str[len(str)-1] == '0' {
				fmt.Printf("📥 Echo: %s\n", str)
			}
		}
	}()

	// 发送循环
	for {
		select {
		case <-sendCtx.Done():
			select {
			case newAddr := <-migrationChan:
				return newAddr, true
			default:
				fmt.Println("🛑 Stopping send loop (Closed)")
				return "", false
			}
		default:
		}

		current := atomic.LoadInt64(seq)
		msg := fmt.Sprintf("Ping-%d", current)
		if current%10 == 0 { // 减少日志输出频率
			fmt.Printf("📤 Sending: %s\n", msg)
		}
		_, err := stream.Write([]byte(msg))
		if err != nil {
			fmt.Println("⚠️ Write failed (Pre-dump?):", err)
			// 失败不要退出，等待 Wrapper 恢复
			time.Sleep(500 * time.Millisecond)
			continue
		}
		atomic.AddInt64(seq, 1)
		time.Sleep(100 * time.Millisecond) // 稍微加快发送频率，方便观察
	}
}
