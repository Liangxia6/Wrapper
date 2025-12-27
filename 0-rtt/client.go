package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"time"

	"github.com/quic-go/quic-go"
)

type MigrationCmd struct {
	Cmd     string `json:"cmd"`
	NewIP   string `json:"new_ip"`
	NewPort int    `json:"new_port"`
}

// 【修复点 1】根据您的报错信息，将返回值改为 *quic.Stream
// 定义一个兼容接口，同时适配 *quic.Conn 和 *quic.EarlyConn
type QuicConnection interface {
	OpenStreamSync(context.Context) (*quic.Stream, error)
	CloseWithError(quic.ApplicationErrorCode, string) error
	Context() context.Context
}

// 全局 Session Cache，这是 0-RTT 的关键
var sessionCache = tls.NewLRUClientSessionCache(100)

func main() {
	tlsConf := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"mec-migration"},
		ClientSessionCache: sessionCache, // 【重要】启用 Session Ticket 缓存
	}

	fmt.Println("🚗 [Vehicle] 启动，连接 MEC-A (127.0.0.1:4242)...")

	// 连接 MEC-A
	ctx := context.Background()
	// DialAddr 返回 *quic.Conn
	session, err := quic.DialAddr(ctx, "127.0.0.1:4242", tlsConf, nil)
	if err != nil {
		panic(err)
	}

	handleConnection(session, tlsConf)

	// 阻塞主线程防止退出
	select {}
}

// 处理连接逻辑
func handleConnection(sess QuicConnection, tlsConf *tls.Config) {
	// 【修复点 2】这里的返回值类型也会自动匹配为 *quic.Stream
	stream, err := sess.OpenStreamSync(context.Background())
	if err != nil {
		return
	}

	fmt.Println("✅ [Vehicle] 已连接到 MEC。开始上报状态...")

	// 启动协程不断发送状态
	go func() {
		for {
			_, err := stream.Write([]byte("Car_Speed_80km/h"))
			if err != nil {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()

	// 监听下行指令
	buf := make([]byte, 1024)
	for {
		n, err := stream.Read(buf)
		if err != nil {
			fmt.Println("❌ [Vehicle] 连接断开 (可能是迁移开始了)")
			return
		}

		// 尝试解析 JSON 指令
		var cmd MigrationCmd
		if json.Unmarshal(buf[:n], &cmd) == nil && cmd.Cmd == "migrate" {
			fmt.Printf("📩 [Vehicle] 收到迁移指令! 目标: %s:%d\n", cmd.NewIP, cmd.NewPort)
			// 触发切换
			go performZeroRTTSwitch(cmd.NewIP, cmd.NewPort, tlsConf)
			return
		}
	}
}

// 执行 0-RTT 切换
func performZeroRTTSwitch(ip string, port int, tlsConf *tls.Config) {
	targetAddr := fmt.Sprintf("%s:%d", ip, port)

	// 1. 记录开始时间
	tStart := time.Now()
	fmt.Printf("⏱️ [计时开始] 收到指令，准备切换...\n")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 2. 发起连接
	newSess, err := quic.DialAddrEarly(ctx, targetAddr, tlsConf, nil)
	if err != nil {
		fmt.Printf("🔥 切换失败: %v\n", err)
		return
	}

	// 3. 0-RTT 数据发送
	newStream, err := newSess.OpenStream()
	if err == nil {
		// 记录 Dial 完成时间
		tDialed := time.Now()

		// 发送业务数据
		payload := fmt.Sprintf("Hello MEC-B! Timestamp: %d", time.Now().UnixNano())
		newStream.Write([]byte(payload))

		// 记录发送完成时间
		tSent := time.Now()

		fmt.Println("------------------------------------------------")
		fmt.Printf("✅ [时延统计]\n")
		fmt.Printf("1. Dial耗时 (建立连接): %v\n", tDialed.Sub(tStart))
		fmt.Printf("2. 0-RTT写耗时 (首包发出): %v\n", tSent.Sub(tDialed))
		fmt.Printf("🚀 总迁移开销 (客户端视角): %v\n", tSent.Sub(tStart))
		fmt.Println("------------------------------------------------")

		handleConnection(newSess, tlsConf)
	}
}
