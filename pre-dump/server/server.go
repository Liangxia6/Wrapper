package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/quic-go/quic-go"
)

func main() {
	// 创建自定义的 PacketConn (Socket Wrapper)
	// 这是实现无感迁移的核心：它允许我们在不通知上层 QUIC 协议栈的情况下，
	// 在底层关闭并重新创建 UDP Socket。
	wrapper, err := NewMigratablePacketConn("0.0.0.0:4433")
	if err != nil {
		panic(err)
	}

	// 配置 QUIC 参数
	config := &quic.Config{
		// MaxIdleTimeout 是关键参数。
		// 在迁移过程中，Socket 会被关闭，导致一段时间内无法通信。
		// 必须将此超时时间设置得比迁移所需时间长（例如 30秒），
		// 否则 QUIC 协议层会认为连接超时而主动断开，导致迁移失败。
		MaxIdleTimeout: 30 * time.Second,
	}

	// 使用自定义的 wrapper 启动 QUIC 监听
	// 注意：这里传入的是 wrapper 而不是普通的 UDPConn
	listener, err := quic.Listen(wrapper, generateTLSConfig(), config)
	if err != nil {
		panic(err)
	}
	fmt.Println("🚀 MEC Server 启动在 :4433 (PID:", os.Getpid(), ")")

	// 启动信号处理协程，监听迁移信号 (SIGUSR1, SIGUSR2)
	go handleSignals(wrapper)

	// 主循环：接受新的 QUIC 连接
	for {
		sess, err := listener.Accept(context.Background())
		if err != nil {
			// 特殊处理：当 Socket 被 wrapper 关闭（为了 Checkpoint）时，Accept 会报错。
			// 我们不能让主程序退出，而是应该静默等待。
			// 当 Restore 完成并 Rebind 后，Accept 可能会恢复（取决于 quic-go 的实现细节，
			// 但通常 Accept 依赖于底层的 ReadFrom，而 wrapper 的 ReadFrom 会阻塞而不是报错，
			// 所以这里的 err 主要是为了防御性编程）。
			fmt.Printf("⚠️ Accept 暂时中断: %v\n", err)

			// 阻塞主线程，防止退出。
			// 实际的恢复逻辑由 handleSignals 中的 Rebind 触发。
			select {}
		}
		// 为每个连接启动单独的处理协程
		go handleSession(sess)
	}
}

// handleSession 处理单个车辆的连接
func handleSession(sess *quic.Conn) {
	// 接受客户端打开的流
	stream, err := sess.AcceptStream(context.Background())
	if err != nil {
		return
	}
	fmt.Printf("✅ 车辆已连接: %s\n", sess.RemoteAddr())

	buf := make([]byte, 1024)
	for {
		// 读取车辆发送的数据
		n, err := stream.Read(buf)
		if err != nil {
			return
		}
		fmt.Printf("📥 收到: %s\n", buf[:n])

		// 发送 ACK 确认
		// 如果此时正在迁移，wrapper 的 WriteTo 会丢弃这个包，
		// 但 QUIC 协议层会负责在连接恢复后重传。
		stream.Write([]byte("MEC_ACK"))
	}
}

// handleSignals 处理系统信号，协调 CRIU 的 Checkpoint/Restore 流程
func handleSignals(w *MigratablePacketConn) {
	sigs := make(chan os.Signal, 1)
	// 监听 SIGUSR1 (开始迁移) 和 SIGUSR2 (迁移完成/恢复)
	signal.Notify(sigs, syscall.SIGUSR1, syscall.SIGUSR2)
	for {
		sig := <-sigs
		switch sig {
		case syscall.SIGUSR1:
			// 收到 SIGUSR1：准备 Checkpoint
			// 1. 标记状态为 isMigrating
			// 2. 关闭底层 UDP Socket (为了让 CRIU 检查通过)
			// 3. 阻塞所有 ReadFrom 调用
			w.PrepareForCheckpoint()
		case syscall.SIGUSR2:
			// 收到 SIGUSR2：Restore 完成
			// 1. 重新绑定端口 (创建新的 UDP Socket)
			// 2. 解除 isMigrating 状态
			// 3. 唤醒所有阻塞的 ReadFrom，恢复通信
			w.Rebind("0.0.0.0:4433")
		}
	}
}

// generateTLSConfig 生成自签名的 TLS 证书
// QUIC 强制要求使用 TLS 1.3
func generateTLSConfig() *tls.Config {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
	}
	certDER, _ := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	tlsCert, _ := tls.X509KeyPair(certPEM, keyPEM)
	return &tls.Config{Certificates: []tls.Certificate{tlsCert}, NextProtos: []string{"mec-migration"}}
}
