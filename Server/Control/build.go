package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

func mustPickGoBin() string {
	const preferred = "/usr/local/go/bin/go"
	if fi, err := os.Stat(preferred); err == nil && !fi.IsDir() && (fi.Mode()&0o111) != 0 {
		return preferred
	}
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	die("missing dependency: go")
	return "go"
}

func goBuildStatic(verbose bool, dir string, goBin string, out string, pkg string) error {
	cmd := exec.Command(goBin, "build", "-o", out, pkg)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s build -o %s %s: %w (%s)", goBin, out, pkg, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
