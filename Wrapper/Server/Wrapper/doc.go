// Package wrapper 实现服务端侧的 QUIC wrapper（运行在容器内部）。
//
// 高层流程：
//   - 在容器内监听 UDP，然后在其上创建 QUIC listener。
//   - 每个连接的第一条 stream 作为控制流。
//   - 后续 stream 作为业务流，由 APP 处理（echo/未来业务等）。
//
// 迁移集成点：
//   - 容器外的 Control 进程发送 SIGTERM，触发服务端向客户端广播 "migrate"，并等待 ACK。
//   - CRIU restore 到容器 B 之后，Control 发送 SIGUSR2，触发 UDP rebind。
//     这是必要的：被恢复的进程需要创建一个“新”的 UDP socket，以匹配新的网络命名空间/端口映射。
//
// 关键类型：MigratableUDP
//   - 提供类似 net.PacketConn 的行为，并支持 Rebind()，且不会让 QUIC listener 直接崩掉。
//   - 这点很关键：quic-go 会并发从 UDP socket 读数据，因此 socket 的 swap 必须非常谨慎。
package wrapper
