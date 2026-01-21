package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var (
	reConnected = regexp.MustCompile(`(?m)^✅ \[Client\] Connected`)
	reMigrate   = regexp.MustCompile(`\[MIGRATION\] migrate:`)
	rePing      = regexp.MustCompile(`\[PING\].*Sending:\s*(.+)$`)
	reEcho      = regexp.MustCompile(`\[ECHO\].*Echo:\s*(.+)\s*\(rtt=([0-9]+)ms\)$`)
	reReconnect = regexp.MustCompile(`\[RECONNECT\]`)
)

func simplifyClientLine(line string) (string, bool) {
	if m := rePing.FindStringSubmatch(line); len(m) == 2 {
		return fmt.Sprintf("发 %s", strings.TrimSpace(m[1])), true
	}
	if m := reEcho.FindStringSubmatch(line); len(m) == 3 {
		msg := strings.TrimSpace(m[1])
		rtt := strings.TrimSpace(m[2])
		return fmt.Sprintf("收 %s rtt=%sms", msg, rtt), true
	}
	return "", false
}

type clientObserver struct {
	connected               chan struct{}
	migrateSeen             chan struct{}
	firstEchoAfterReconnect chan struct{}

	stopFn func()

	lastEchoBeforeOutage time.Time
	firstEchoRecovered   time.Time
	sawReconnect         bool
}

func startClientObserver(cmd *exec.Cmd, logPath string) (*clientObserver, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	f, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}

	obs := &clientObserver{
		connected:               make(chan struct{}, 1),
		migrateSeen:             make(chan struct{}, 1),
		firstEchoAfterReconnect: make(chan struct{}, 1),
		stopFn:                  func() { _ = f.Close() },
	}

	if err := cmd.Start(); err != nil {
		_ = f.Close()
		return nil, err
	}

	parse := func(r io.Reader) {
		s := bufio.NewScanner(r)
		buf := make([]byte, 0, 64*1024)
		s.Buffer(buf, 1024*1024)
		for s.Scan() {
			line := s.Text()
			now := time.Now()
			_, _ = f.WriteString(line + "\n")

			if reConnected.MatchString(line) {
				select {
				case obs.connected <- struct{}{}:
				default:
				}
			}
			if reMigrate.MatchString(line) {
				select {
				case obs.migrateSeen <- struct{}{}:
				default:
				}
				obs.sawReconnect = true
			}
			if reReconnect.MatchString(line) {
				obs.sawReconnect = true
			}

			if out, ok := simplifyClientLine(line); ok {
				fmt.Printf("[客户端] %s\n", out)
				if strings.HasPrefix(out, "收 ") {
					if !obs.sawReconnect {
						obs.lastEchoBeforeOutage = now
					} else if obs.firstEchoRecovered.IsZero() {
						obs.firstEchoRecovered = now
						select {
						case obs.firstEchoAfterReconnect <- struct{}{}:
						default:
						}
					}
				}
			}
		}
	}

	go parse(stdout)
	go parse(stderr)

	go func() {
		_ = cmd.Wait()
		_ = f.Close()
	}()

	return obs, nil
}

func (o *clientObserver) stop() {
	if o.stopFn != nil {
		o.stopFn()
	}
}

func (o *clientObserver) downtime() time.Duration {
	if o.lastEchoBeforeOutage.IsZero() || o.firstEchoRecovered.IsZero() {
		return -1
	}
	return o.firstEchoRecovered.Sub(o.lastEchoBeforeOutage)
}
