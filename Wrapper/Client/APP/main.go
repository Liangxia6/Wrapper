package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Liangxia6/Wrapper/Client/cWrapper"
)

func main() {
	var target string
	var interval time.Duration
	var intervalAfterMigrate time.Duration
	var ioTimeout time.Duration
	var ioTimeoutAfterMigrate time.Duration
	var dialTimeout time.Duration
	var dialBackoff time.Duration
	var quiet bool
	var stayConnected bool

	flag.StringVar(&target, "target", envOr("TARGET_ADDR", "127.0.0.1:5242"), "server addr")
	flag.DurationVar(&interval, "interval", 200*time.Millisecond, "ping interval")
	flag.DurationVar(&intervalAfterMigrate, "interval-after-migrate", 20*time.Millisecond, "ping interval after migrate")
	flag.DurationVar(&ioTimeout, "io-timeout", 1200*time.Millisecond, "per-ping io timeout")
	flag.DurationVar(&ioTimeoutAfterMigrate, "io-timeout-after-migrate", 250*time.Millisecond, "per-ping io timeout after migrate")
	flag.DurationVar(&dialTimeout, "dial-timeout", 900*time.Millisecond, "per-dial timeout")
	flag.DurationVar(&dialBackoff, "dial-backoff", 50*time.Millisecond, "dial retry backoff")
	flag.BoolVar(&quiet, "quiet", false, "reduce logs")
	flag.BoolVar(&stayConnected, "stay-connected", false, "do not end session on io errors; reopen stream and keep trying")
	flag.Parse()

	if strings.TrimSpace(os.Getenv("STAY_CONNECTED")) != "" {
		stayConnected = true
	}
	transparent := strings.TrimSpace(os.Getenv("TRANSPARENT"))
	if transparent != "" {
		stayConnected = true
	}

	m := &wrapper.Manager{Target: target, Quiet: quiet, ClientID: "car", DialTimeout: dialTimeout, DialBackoff: dialBackoff}

	var lastEchoBeforeOutage time.Time
	var awaitingFirstAfter bool

	_ = m.Run(context.Background(), func(ctx context.Context, s *wrapper.Session) error {
		openData := func() (io.ReadWriteCloser, *bufio.Reader, *bufio.Writer, any, error) {
			st, err := s.Conn.OpenStreamSync(ctx)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			type deadlineSetter interface {
				SetReadDeadline(time.Time) error
				SetWriteDeadline(time.Time) error
			}
			ds, _ := any(st).(deadlineSetter)
			r := bufio.NewReader(st)
			w := bufio.NewWriter(st)
			return st, r, w, ds, nil
		}

		data, r, w, dsAny, err := openData()
		if err != nil {
			return err
		}
		defer data.Close()

		pingID := 0
		curIOTimeout := ioTimeout
		curInterval := interval
		migrated := false
		for {
			if !migrated {
				select {
				case <-s.MigrateSeen:
					migrated = true
					wrapper.Tracef("app migrateSeen")
					if ioTimeoutAfterMigrate > 0 && ioTimeoutAfterMigrate < curIOTimeout {
						curIOTimeout = ioTimeoutAfterMigrate
					}
					if intervalAfterMigrate > 0 && intervalAfterMigrate < curInterval {
						curInterval = intervalAfterMigrate
					}
				default:
				}
			}

			select {
			case <-ctx.Done():
				// 这里代表“迁移触发或 session 结束”，把 outage 的起点记在最后一次 echo。
				if !lastEchoBeforeOutage.IsZero() {
					awaitingFirstAfter = true
					wrapper.Tracef("app session end; awaitingFirstAfter=true")
				}
				return nil
			default:
			}

			payload := fmt.Sprintf("Ping-%d", pingID)
			pingID++

			if !quiet {
				fmt.Printf("[PING] Sending: %s\n", payload)
			}

			start := time.Now()
			ds, _ := dsAny.(interface{
				SetReadDeadline(time.Time) error
				SetWriteDeadline(time.Time) error
			})
			if ds != nil {
				_ = ds.SetWriteDeadline(start.Add(curIOTimeout))
			}
			if _, err := w.WriteString(payload + "\n"); err != nil {
				if !lastEchoBeforeOutage.IsZero() && !awaitingFirstAfter {
					awaitingFirstAfter = true
					wrapper.Tracef("app write err; awaitingFirstAfter=true err=%v", err)
				}
				if !stayConnected {
					return nil
				}
				_ = data.Close()
				data, r, w, dsAny, _ = openData()
				time.Sleep(10 * time.Millisecond)
				continue
			}
			if err := w.Flush(); err != nil {
				if !lastEchoBeforeOutage.IsZero() && !awaitingFirstAfter {
					awaitingFirstAfter = true
					wrapper.Tracef("app flush err; awaitingFirstAfter=true err=%v", err)
				}
				if !stayConnected {
					return nil
				}
				_ = data.Close()
				data, r, w, dsAny, _ = openData()
				time.Sleep(10 * time.Millisecond)
				continue
			}

			if ds != nil {
				_ = ds.SetReadDeadline(time.Now().Add(curIOTimeout))
			}
			echoLine, err := r.ReadString('\n')
			if err != nil {
				if !lastEchoBeforeOutage.IsZero() && !awaitingFirstAfter {
					awaitingFirstAfter = true
					wrapper.Tracef("app read err; awaitingFirstAfter=true err=%v", err)
				}
				if !stayConnected {
					return nil
				}
				_ = data.Close()
				data, r, w, dsAny, _ = openData()
				time.Sleep(10 * time.Millisecond)
				continue
			}
			echo := strings.TrimSpace(echoLine)
			rtt := time.Since(start)

			now := time.Now()
			if awaitingFirstAfter {
				dt := now.Sub(lastEchoBeforeOutage)
				fmt.Printf("[客户端] 汇总：服务中断 %dms\n", dt.Milliseconds())
				wrapper.Tracef("app recovered; downtime=%dms", dt.Milliseconds())
				awaitingFirstAfter = false
			}
			lastEchoBeforeOutage = now

			if !quiet {
				fmt.Printf("[ECHO] Echo: %s (rtt=%dms)\n", echo, rtt.Milliseconds())
			}

			time.Sleep(curInterval)
		}
	})
}

func envOr(k, def string) string {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	return v
}
