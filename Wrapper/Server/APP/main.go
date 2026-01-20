package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Liangxia6/Wrapper/Server/Wrapper"
)

// 说明：这是“被迁移的服务端应用（App）”的 demo 实现：
// 只关心业务数据流（当前为 echo；未来可替换成 AI 业务）。
// QUIC、控制流（migrate/ack）、信号处理、可迁移 UDP 都由 Server/Wrapper 负责。
func main() {
	opts := wrapper.DefaultServerOptions()
	if err := wrapper.Serve(context.Background(), opts, handleEcho); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
