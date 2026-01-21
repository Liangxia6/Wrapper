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
	TypeCommit  MessageType = "commit"
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

// 控制流协议：以换行分隔的 JSON（newline-delimited JSON）。
//
// 为什么在 QUIC stream 里用 "JSON + \n"？
//   - PoC 阶段实现简单，日志可读性好。
//   - bufio.Scanner 可按行切分，解析逻辑直观。
//   - QUIC stream 自带可靠有序传输。
//
// 注意：Scanner 有 token 长度限制，因此 NewLineReader 会调大 buffer。

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
	// 允许单行最大 1MiB，避免 Scanner 因 token 过大而拒绝。
	// 控制消息本身很小，但这样更稳健。
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
