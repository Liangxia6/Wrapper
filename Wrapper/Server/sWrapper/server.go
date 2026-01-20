package wrapper

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/quic-go/quic-go"
)

// ServerOptions 描述“容器内运行的服务端 wrapper”行为。
// APP 只需要提供数据流处理函数（例如 echo/AI 业务）。
//
// 迁移信号、控制流协议、QUIC listener、可迁移 UDP rebind 都在这里实现。

type ServerOptions struct {
	ListenAddr string

	// migrate 指令里推送给 client 的新地址/端口。
	MigrateAddr string
	MigratePort int

	Quiet bool

	KeepAlivePeriod time.Duration
	AckTimeout      time.Duration
}

func DefaultServerOptions() ServerOptions {
	return ServerOptions{
		ListenAddr:       envOr("LISTEN_ADDR", ":4242"),
		MigrateAddr:      envOr("MIGRATE_ADDR", "127.0.0.1"),
		MigratePort:      envOrInt("MIGRATE_PORT", 5243),
		Quiet:            envOrBool("QUIET", true),
		KeepAlivePeriod:  2 * time.Second,
		AckTimeout:       800 * time.Millisecond,
	}
}

// Serve 启动 server wrapper：
// - 建立 QUIC listener
// - 每个连接第一条 stream 作为控制流（migrate/ack）
// - 后续 stream 交给 APP 提供的 handler
// - SIGTERM 触发 migrate 广播并等待 ACK（PoC/Control 用）
// - SIGUSR2 触发 UDP Rebind（CRIU restore 后用）
func Serve(ctx context.Context, opts ServerOptions, handler func(stream io.ReadWriteCloser)) error {
	if handler == nil {
		return fmt.Errorf("handler is nil")
	}
	if opts.ListenAddr == "" {
		opts.ListenAddr = ":4242"
	}
	if opts.KeepAlivePeriod <= 0 {
		opts.KeepAlivePeriod = 2 * time.Second
	}
	if opts.AckTimeout <= 0 {
		opts.AckTimeout = 800 * time.Millisecond
	}

	tlsConf, err := ServerTLSConfig()
	if err != nil {
		return fmt.Errorf("tls: %w", err)
	}

	udpAddr, err := net.ResolveUDPAddr("udp", opts.ListenAddr)
	if err != nil {
		return fmt.Errorf("resolve listen: %w", err)
	}
	pc, err := ListenMigratableUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("listen udp: %w", err)
	}
	defer pc.Close()

	listener, err := quic.Listen(pc, tlsConf, &quic.Config{KeepAlivePeriod: opts.KeepAlivePeriod})
	if err != nil {
		return fmt.Errorf("quic listen: %w", err)
	}
	defer listener.Close()

	if !opts.Quiet {
		fmt.Printf("[服务端] 监听 %s\n", opts.ListenAddr)
	}

	// 容器内协作点：restore 后由 Control 发 SIGUSR2 来触发 rebind。
	stopUSR2 := InstallRebindOnUSR2(pc)
	defer stopUSR2()

	var (
		mu  sync.Mutex
		cur *ControlClient
	)

	register := func(c *ControlClient) {
		mu.Lock()
		defer mu.Unlock()
		cur = c
	}
	unregister := func(c *ControlClient) {
		mu.Lock()
		defer mu.Unlock()
		if cur == c {
			cur = nil
		}
	}

	// SIGTERM: 触发 migrate 广播（供 Control 在容器外编排时使用）。
	term := make(chan os.Signal, 2)
	signal.Notify(term, syscall.SIGTERM)
	defer signal.Stop(term)

	go func() {
		for range term {
			id := fmt.Sprintf("m-%d", time.Now().UnixNano())
			mu.Lock()
			c := cur
			mu.Unlock()
			if c == nil {
				if !opts.Quiet {
					fmt.Printf("[服务端] 触发迁移 id=%s (no active client)\n", id)
				}
				continue
			}
			if !opts.Quiet {
				fmt.Printf("[服务端] 触发迁移 id=%s new=%s:%d\n", id, opts.MigrateAddr, opts.MigratePort)
			}
			wait, ok := c.SendMigrateAndWait(id, opts.MigrateAddr, opts.MigratePort, opts.AckTimeout)
			if !opts.Quiet {
				if ok {
					fmt.Printf("[服务端] 收到ACK id=%s wait=%dms\n", id, wait.Milliseconds())
				} else {
					fmt.Printf("[服务端] ACK超时 id=%s wait=%dms\n", id, wait.Milliseconds())
				}
			}
		}
	}()

	// 退出时关闭 listener 让 Accept 退出。
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// quic-go 关闭时的错误信息可能各版本不同，这里做弱判断。
			if strings.Contains(strings.ToLower(err.Error()), "closed") {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}

		go func(conn quic.Connection) {
			// 约定：client 第一条双向 stream 为控制流。
			ctrl, err := conn.AcceptStream(context.Background())
			if err != nil {
				return
			}

			cc := NewControlClient(ctrl)
			cc.Start()
			register(cc)
			defer unregister(cc)

			// 后续 stream：业务数据流（由 APP 处理）。
			for {
				st, err := conn.AcceptStream(context.Background())
				if err != nil {
					return
				}
				go handler(st)
			}
		}(conn)
	}
}

func envOr(k, def string) string {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	return v
}

func envOrInt(k string, def int) int {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envOrBool(k string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	v = strings.ToLower(v)
	return v == "1" || v == "true" || v == "yes" || v == "y"
}
