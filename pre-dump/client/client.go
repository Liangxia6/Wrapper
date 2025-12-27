package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/quic-go/quic-go"
)

// QuicConnection 定义了我们需要的 QUIC 连接接口，方便后续可能的扩展或 Mock 测试
// 这里主要使用了 OpenStreamSync (同步打开流), CloseWithError (关闭连接), Context (获取上下文)
type QuicConnection interface {
	OpenStreamSync(context.Context) (*quic.Stream, error)
	CloseWithError(quic.ApplicationErrorCode, string) error
	Context() context.Context
}

// sessionCache 用于缓存 TLS 会话票据 (Session Ticket)
// 开启 Session Cache 是支持 QUIC 0-RTT (Zero Round Trip Time) 重连的关键
// 当客户端重新连接时，如果能复用之前的 Session Ticket，就可以在握手完成前发送数据
var sessionCache = tls.NewLRUClientSessionCache(100)

func main() {
	// 配置 TLS
	// InsecureSkipVerify: true -> 跳过证书验证（仅用于测试环境，生产环境请使用正规证书）
	// NextProtos: 指定应用层协议名称，服务端和客户端必须一致才能协商成功
	// ClientSessionCache: 启用会话缓存，为 0-RTT 做准备
	tlsConf := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"mec-migration"},
		ClientSessionCache: sessionCache,
	}

	fmt.Println("🚗 车辆启动，连接 MEC (127.0.0.1:4433)...")

	// 建立 QUIC 连接
	// 这里的 IP 应该是 MEC 服务端的地址。在容器环境中，可能是容器 IP 或映射后的 Host IP。
	// quic.DialAddr 会自动完成 UDP Socket 的创建和 QUIC 握手
	ctx := context.Background()
	session, err := quic.DialAddr(ctx, "127.0.0.1:4433", tlsConf, nil)
	if err != nil {
		panic(err) // 如果连接失败，直接崩溃退出（实际项目中应有重试逻辑）
	}

	// 处理连接逻辑
	handleConnection(session)

	// 阻塞主线程，防止程序退出
	select {}
}

// handleConnection 处理与服务端的交互逻辑
func handleConnection(sess QuicConnection) {
	// 打开一个双向流 (Stream)
	// OpenStreamSync 会阻塞直到流成功打开
	stream, err := sess.OpenStreamSync(context.Background())
	if err != nil {
		return
	}

	fmt.Println("✅ 已连接，开始发送数据...")

	// 启动一个协程，模拟车辆持续发送数据
	go func() {
		i := 0
		for {
			msg := fmt.Sprintf("Car_Data_%d", i)
			// 向流中写入数据
			_, err := stream.Write([]byte(msg))
			if err != nil {
				// 错误处理逻辑：
				// 当服务端正在迁移时，Socket 可能暂时不可达，导致写入失败。
				// 这里模拟了简单的重试机制：打印错误并等待，而不是直接断开连接。
				// 在 QUIC 协议层面，如果连接未断开，重试写入通常能恢复。
				fmt.Println("❌ 发送失败 (可能是服务端正在迁移):", err)
				time.Sleep(500 * time.Millisecond) // 等待服务端恢复
				continue
			}
			fmt.Printf("📤 发送: %s\n", msg)
			i++
			// 模拟数据发送间隔
			time.Sleep(500 * time.Millisecond)
		}
	}()

	// 主协程负责读取服务端的回包
	// 这是一个简单的 Echo 确认机制
	buf := make([]byte, 1024)
	for {
		_, err := stream.Read(buf)
		if err != nil {
			return
		} // 读取失败通常意味着连接断开
	}
}
