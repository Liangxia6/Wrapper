// Command proxy 是一个极简 UDP 转发器，用于让“迁移对 QUIC 透明”。
//
// 客户端始终把 QUIC dial 到 LISTEN_ADDR（即本 proxy）。
// 控制端（Control）把当前后端地址（"ip:port"）写入 BACKEND_FILE。
// proxy 轮询该文件，一旦内容变化就切换转发目标。
//
// 因此在 A -> B 迁移期间：
//   - 客户端 target 保持不变（无需 QUIC 重连/切 target）。
//   - 后端变化被隐藏在 QUIC 之下（纯 UDP 转发）。
//
// 这是 PoC 级实现：
//   - 单客户端映射（记住“最后一个见到的客户端地址”）。
//   - 不做鉴权。
//   - 不实现额外的丢包恢复（依赖 QUIC 本身的可靠性机制）。
package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type backendAddr struct {
	// addr 是当前选择的后端 UDP 地址（例如 127.0.0.1:5242）。
	// 转发热路径会高频读取它。
	addr *net.UDPAddr
	// err 主要用于调试/后续指标；当前代码只判断 addr != nil。
	err  error
}

func main() {
	listenAddr := envOr("LISTEN_ADDR", ":5342")
	backendFile := envOr("BACKEND_FILE", "/dev/shm/criu-inject/backend.addr")
	poll := envOrDuration("BACKEND_POLL", 20*time.Millisecond)

	lc, err := net.ListenUDP("udp", mustResolveUDP(listenAddr))
	fatalIf(err, "listen client")
	defer lc.Close()

	// Backend socket: keep a stable local UDP port towards backends.
	bc, err := net.ListenUDP("udp", nil)
	fatalIf(err, "listen backend")
	defer bc.Close()

	// 我们使用两个 UDP socket：
	//   - lc：面向客户端的 socket（固定 LISTEN_ADDR）
	//   - bc：面向后端的 socket（对后端保持稳定的本地端口）
	//
	// 对后端保持稳定本地端口的好处：
	//   - 避免额外的 NAT 抖动
	//   - 更利于抓包/日志定位
	fmt.Printf("[proxy] listen=%s backendSock=%s backendFile=%s\n", lc.LocalAddr().String(), bc.LocalAddr().String(), backendFile)

	var cur atomic.Value
	cur.Store(backendAddr{addr: nil, err: fmt.Errorf("no backend yet")})

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		watchBackendFile(backendFile, poll, &cur, stop)
	}()

	// 单客户端映射（对当前 PoC 足够）。
	//
	// 限制：
	//   - 我们记住“最后一次见到的客户端地址”，并把后端回包发给它。
	//   - 这对当前 MEC vehicle demo（单客户端）成立。
	//   - 若要支持多客户端，需要按 4 元组/连接标识维护映射表。
	var clientMu sync.Mutex
	var lastClient *net.UDPAddr

	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 64*1024)
		for {
			n, from, err := lc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			clientMu.Lock()
			lastClient = from
			clientMu.Unlock()

			b := cur.Load().(backendAddr)
			if b.addr == nil {
				continue
			}
			_, _ = bc.WriteToUDP(buf[:n], b.addr)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 64*1024)
		for {
			n, _, err := bc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			clientMu.Lock()
			c := lastClient
			clientMu.Unlock()
			if c == nil {
				continue
			}
			_, _ = lc.WriteToUDP(buf[:n], c)
		}
	}()

	// Block forever.
	select {}
}

func watchBackendFile(path string, poll time.Duration, cur *atomic.Value, stop <-chan struct{}) {
	// 该 goroutine 轮询一个简单文本文件（内容为 "ip:port"），
	// 当文件内容变化时切换当前 backend。
	//
	// 为什么用文件？
	//   - Control 进程更新它很方便。
	//   - PoC 阶段可以避免再引入一条额外控制通道。
	var last string
	for {
		select {
		case <-stop:
			return
		case <-time.After(poll):
		}

		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(b))
		if s == "" || s == last {
			continue
		}
		addr, rerr := net.ResolveUDPAddr("udp", s)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "[proxy] bad backend %q: %v\n", s, rerr)
			continue
		}
		cur.Store(backendAddr{addr: addr, err: nil})
		last = s
		fmt.Printf("[proxy] backend=%s\n", s)
	}
}

func mustResolveUDP(s string) *net.UDPAddr {
	a, err := net.ResolveUDPAddr("udp", s)
	fatalIf(err, "resolve")
	return a
}

func envOr(k, def string) string {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	return v
}

func envOrDuration(k string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func fatalIf(err error, msg string) {
	if err == nil {
		return
	}
	w := bufio.NewWriter(os.Stderr)
	_, _ = fmt.Fprintf(w, "[proxy] %s: %v\n", msg, err)
	_ = w.Flush()
	os.Exit(1)
}
