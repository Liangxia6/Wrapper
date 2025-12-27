package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/quic-go/quic-go"
)

type MigrationCmd struct {
	Cmd     string `json:"cmd"`
	NewIP   string `json:"new_ip"`
	NewPort int    `json:"new_port"`
}

// 全局变量，用于在 Final Checkpoint 时通知所有连接
var (
	activeStreams []quic.Stream
	streamsMu     sync.Mutex
)

func main() {
	// 1. 创建 Wrapper
	wrapper, err := NewMigratablePacketConn("0.0.0.0:4242")
	if err != nil {
		panic(err)
	}

	// 2. 启动 QUIC 监听
	tlsConf := generateTLSConfig()
	listener, err := quic.Listen(wrapper, tlsConf, &quic.Config{
		MaxIdleTimeout: 60 * time.Second, // 必须足够长，容忍 Pre-dump 期间的断连
	})
	if err != nil {
		panic(err)
	}

	fmt.Printf("🚀 MEC Server (PID: %d) listening on :4242\n", os.Getpid())

	// 3. 启动信号处理 (核心逻辑)
	go handleSignals(wrapper)

	// 4. 接受连接
	for {
		sess, err := listener.Accept(context.Background())
		if err != nil {
			// Wrapper 关闭 Socket 时 Accept 会报错，忽略并等待 Rebind
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go handleSession(sess)
	}
}

func handleSession(sess quic.Connection) {
	stream, err := sess.AcceptStream(context.Background())
	if err != nil {
		return
	}
	fmt.Printf("✅ Client connected: %s\n", sess.RemoteAddr())

	// 注册流，以便后续广播指令
	streamsMu.Lock()
	activeStreams = append(activeStreams, stream)
	streamsMu.Unlock()

	// Echo Loop
	buf := make([]byte, 1024)
	for {
		n, err := stream.Read(buf)
		if err != nil {
			return
		}
		// 回显数据
		stream.Write(buf[:n])
	}
}

func handleSignals(w *MigratablePacketConn) {
	sigs := make(chan os.Signal, 1)
	// SIGUSR1: Pre-dump 开始 (闪断)
	// SIGUSR2: Pre-dump 结束 (恢复)
	// SIGTERM: Final Checkpoint (迁移)
	signal.Notify(sigs, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGTERM)

	for {
		sig := <-sigs
		switch sig {
		case syscall.SIGUSR1:
			fmt.Println("⚡️ 收到 SIGUSR1: 准备 Pre-dump (关闭 Socket)...")
			// 强制 GC 并释放内存给 OS
			debug.FreeOSMemory()
			w.PrepareForCheckpoint()

		case syscall.SIGUSR2:
			fmt.Println("⚡️ 收到 SIGUSR2: Pre-dump 完成 (恢复 Socket)...")
			w.Rebind("0.0.0.0:4242")

		case syscall.SIGTERM:
			fmt.Println("⚡️ 收到 SIGTERM: 准备最终迁移 (通知客户端)...")
			broadcastMigration()
			// 强制 GC 并释放内存给 OS
			debug.FreeOSMemory()
			// 给一点时间让指令发出去，50ms 足够本地网络传输
			time.Sleep(200 * time.Millisecond)
			// 此时 CRIU 会介入进行最终 Dump
			// 不要主动退出，等待 CRIU 冻结并杀死进程
			fmt.Println("⚡️ 等待 Checkpoint...")
			
			// 如果代码执行到这里，说明是从 Checkpoint 恢复了 (或者 CRIU 失败了)
			// 必须确保 Socket 可用。如果 CRIU 恢复了 Socket FD，这里可能不需要做太多。
			// 但为了保险，我们可以打印一条日志。
			time.Sleep(100 * time.Millisecond) // 稍微等一下
			fmt.Println("♻️ [Server] 从 Checkpoint 恢复运行! (Resumed)")
		}
	}
}

func broadcastMigration() {
	streamsMu.Lock()
	defer streamsMu.Unlock()

	cmd := MigrationCmd{
		Cmd:     "migrate",
		NewIP:   "10.0.2.100", // 假设的新 IP，实际场景可由外部配置注入
		NewPort: 4242,
	}
	bytes, _ := json.Marshal(cmd)

	fmt.Printf("📢 广播迁移指令给 %d 个客户端...\n", len(activeStreams))
	for i, s := range activeStreams {
		_, err := s.Write(bytes)
		if err != nil {
			fmt.Printf("❌ 发送指令给 Client-%d 失败: %v\n", i, err)
		} else {
			fmt.Printf("✅ 指令已发送给 Client-%d\n", i)
		}
	}
}

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
