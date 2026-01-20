package wrapper

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// 控制流协议：JSON + \n 分帧。
// - server -> client: migrate
// - client -> server: ack

type MessageType string

const (
	TypeHello   MessageType = "hello"
	TypeMigrate MessageType = "migrate"
	TypeAck     MessageType = "ack"
)

type Message struct {
	Type MessageType `json:"type"`
	ID   string      `json:"id,omitempty"`

	// hello
	ClientID string `json:"client_id,omitempty"`

	// migrate
	NewAddr string `json:"new_addr,omitempty"`
	NewPort int    `json:"new_port,omitempty"`

	// ack
	AckID string `json:"ack_id,omitempty"`
}

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
