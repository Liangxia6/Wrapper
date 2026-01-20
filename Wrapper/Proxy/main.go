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
	addr *net.UDPAddr
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

	// Single-client mapping (good enough for this PoC).
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
