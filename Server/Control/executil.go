package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

func runQuiet(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w (%s)", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func reportExecFailure(start time.Time, out, errOut []byte, err error) {
	fmt.Fprintf(os.Stderr, "[失败] restore 失败（%dms）：%v\n", time.Since(start).Milliseconds(), err)
	if len(out) > 0 {
		fmt.Fprintln(os.Stderr, "--- 标准输出 ---")
		_, _ = io.Copy(os.Stderr, bytes.NewReader(limit(out, 6000)))
		fmt.Fprintln(os.Stderr)
	}
	if len(errOut) > 0 {
		fmt.Fprintln(os.Stderr, "--- 标准错误 ---")
		_, _ = io.Copy(os.Stderr, bytes.NewReader(limit(errOut, 12000)))
		fmt.Fprintln(os.Stderr)
	}
}

func limit(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[len(b)-n:]
}
