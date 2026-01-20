package wrapper

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

type MessageType string

const (
	TypeHello   MessageType = "hello"
	TypeMigrate MessageType = "migrate"
	TypeAck     MessageType = "ack"
)

type Message struct {
	Type MessageType `json:"type"`
	ID   string      `json:"id,omitempty"`

	ClientID string `json:"client_id,omitempty"`

	NewAddr string `json:"new_addr,omitempty"`
	NewPort int    `json:"new_port,omitempty"`

	AckID string `json:"ack_id,omitempty"`
}

// Control protocol: newline-delimited JSON.
//
// Why "JSON + \n" in a QUIC stream?
//   - It's simple for a PoC and debuggable via logs.
//   - bufio.Scanner can split by lines, which keeps parsing straightforward.
//   - QUIC provides reliability and ordering within a stream.
//
// Note: Scanner has a token size limit, so NewLineReader increases the buffer.

func WriteLine(w io.Writer, msg Message) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

type LineReader struct{ s *bufio.Scanner }

func NewLineReader(r io.Reader) *LineReader {
	s := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	// Allow up to 1 MiB per control line to avoid Scanner rejecting larger messages.
	// Our control messages are tiny, but this prevents accidental failures.
	s.Buffer(buf, 1024*1024)
	return &LineReader{s: s}
}

func (lr *LineReader) Next() (Message, bool, error) {
	if !lr.s.Scan() {
		if err := lr.s.Err(); err != nil {
			return Message{}, false, err
		}
		return Message{}, false, nil
	}
	var msg Message
	if err := json.Unmarshal(lr.s.Bytes(), &msg); err != nil {
		return Message{}, true, fmt.Errorf("bad control message: %w", err)
	}
	return msg, true, nil
}
