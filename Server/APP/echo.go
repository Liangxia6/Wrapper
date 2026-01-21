package main

import (
	"bufio"
	"io"
)

func handleEcho(st io.ReadWriteCloser) {
	defer st.Close()

	r := bufio.NewReader(st)
	w := bufio.NewWriter(st)
	defer w.Flush()

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return
			}
			return
		}
		if _, err := w.WriteString(line); err != nil {
			return
		}
		if err := w.Flush(); err != nil {
			return
		}
	}
}
