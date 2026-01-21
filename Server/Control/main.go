package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		runCmd(os.Args[2:])
	case "up":
		upCmd(os.Args[2:])
	case "migrate":
		migrateCmd(os.Args[2:])
	case "down":
		downCmd(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  sudo ./control up --img-dir /dev/shm/criu-inject --criu-host-bin /usr/local/sbin/criu-4.1.1")
	fmt.Fprintln(os.Stderr, "  sudo ./control migrate --img-dir /dev/shm/criu-inject --criu-host-bin /usr/local/sbin/criu-4.1.1")
	fmt.Fprintln(os.Stderr, "  sudo ./control down --img-dir /dev/shm/criu-inject")
	fmt.Fprintln(os.Stderr, "  sudo ./control run --img-dir /dev/shm/criu-inject --criu-host-bin /usr/local/sbin/criu-4.1.1")
}
