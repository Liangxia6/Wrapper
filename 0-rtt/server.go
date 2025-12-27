package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"math/big"
	"time"

	"github.com/quic-go/quic-go"
)

// 定义迁移指令格式
type MigrationCmd struct {
	Cmd     string `json:"cmd"`
	NewIP   string `json:"new_ip"`
	NewPort int    `json:"new_port"`
}

func main() {
	port := flag.Int("port", 4242, "Server port")
	isSource := flag.Bool("source", false, "Is this the Source MEC (MEC-A)?")
	flag.Parse()

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	fmt.Printf("🚀 MEC 服务启动在 %s (Source节点: %v)\n", addr, *isSource)

	// 1. 生成 TLS 配置
	tlsConf := generateTLSConfig()

	// 2. 监听 QUIC
	// 【修复 1】移除了 AllowConnectionMigration (新版默认支持或已移除该字段)
	listener, err := quic.ListenAddr(addr, tlsConf, &quic.Config{})
	if err != nil {
		panic(err)
	}

	for {
		sess, err := listener.Accept(context.Background())
		if err != nil {
			fmt.Println("Accept error:", err)
			continue
		}
		// 每个连接启动一个协程处理
		go handleSession(sess, *isSource)
	}
}

// 【修复 2】类型改为 *quic.Conn (新版 Accept 返回的是结构体指针)
func handleSession(sess *quic.Conn, isSource bool) {
	// 等待车辆建立 Stream
	stream, err := sess.AcceptStream(context.Background())
	if err != nil {
		return
	}
	defer stream.Close()

	fmt.Printf("✅ [MEC] 车辆已连接! RemoteAddr: %s\n", sess.RemoteAddr())

	if isSource {
		// === 模拟 MEC-A (源节点) ===
		go func() {
			buf := make([]byte, 1024)
			for {
				n, err := stream.Read(buf)
				if err != nil {
					return
				}
				fmt.Printf("📥 [MEC-A] 收到车辆上报: %s\n", buf[:n])
			}
		}()

		// 模拟 3秒后触发迁移
		fmt.Println("⏳ [MEC-A] 正常服务中... 3秒后触发迁移指令...")
		time.Sleep(3 * time.Second)

		fmt.Println("⚠️ [MEC-A] 发送迁移指令! 目标 -> MEC-B (:4243)")

		cmd := MigrationCmd{
			Cmd:     "migrate",
			NewIP:   "127.0.0.1",
			NewPort: 4243,
		}
		bytes, _ := json.Marshal(cmd)
		stream.Write(bytes)

		// 给一点时间让指令发出去，然后模拟 CRIU 冻结
		time.Sleep(100 * time.Millisecond)
		fmt.Println("🛑 [MEC-A] 模拟 CRIU 冻结，关闭连接")

		// 错误码 0x0 对应 NoError
		sess.CloseWithError(0x0, "migration_triggered")

	} else {
		// === 模拟 MEC-B (目标节点) ===
		buf := make([]byte, 1024)
		n, err := stream.Read(buf)
		if err == nil {
			// 如果是 0-RTT，这里会立即读到数据
			fmt.Printf("⚡️ [MEC-B] 收到 0-RTT/Early Data: %s\n", buf[:n])
			stream.Write([]byte("MEC-B: Welcome! Handover Complete."))
		}
		// 保持连接不退出
		select {}
	}
}

// 辅助代码：生成自签名证书
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
	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"mec-migration"},
	}
}
