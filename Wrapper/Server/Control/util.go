package main

import (
	"fmt"
	"os"
)

func die(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(2)
}

func dief(format string, args ...any) {
	die(fmt.Sprintf(format, args...))
}
