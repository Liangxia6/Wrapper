package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func podmanStatePID(name string) (int, error) {
	cmd := exec.Command("sudo", "podman", "inspect", "-f", "{{.State.Pid}}", name)
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("invalid pid: %q", strings.TrimSpace(string(out)))
	}
	return pid, nil
}
