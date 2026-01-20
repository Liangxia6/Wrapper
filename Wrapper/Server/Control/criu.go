package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func pickCRIUHostBin(override string) (string, error) {
	if override != "" {
		if fi, err := os.Stat(override); err == nil && !fi.IsDir() {
			return override, nil
		}
		return "", fmt.Errorf("not found: %s", override)
	}
	paths := []string{"/usr/local/sbin/criu-4.1.1", "/usr/local/sbin/criu"}
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	if p, err := exec.LookPath("criu"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("criu not found")
}

func sudoKill0(pid int) error {
	return exec.Command("sudo", "kill", "-0", strconv.Itoa(pid)).Run()
}

func sudoKill(pid int, sig syscall.Signal) error {
	return exec.Command("sudo", "kill", fmt.Sprintf("-%d", sig), strconv.Itoa(pid)).Run()
}

func readPIDFile(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(b))
	pid, err := strconv.Atoi(s)
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("invalid pid: %q", s)
	}
	return pid, nil
}
