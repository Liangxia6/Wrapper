// Package wrapper 实现客户端侧的 QUIC “wrapper”。
//
// 设计目标（当前主线）：迁移对 QUIC 透明。
//
// 传统“重连式迁移”做法是：客户端收到 "migrate" 控制消息后，dial 到新地址/端口，
// 通过重建 QUIC session 完成切换。它很快（尤其配合 0-RTT），但 QUIC 并不透明。
//
// 本项目的“透明模式（内部 UDP 解耦）”做法：
//   - 客户端为 quic-go 提供一个稳定的 net.PacketConn（SwappableUDPConn）。
//   - 迁移时通过控制流拿到新后端地址，然后 SwappableUDPConn.SetPeer() 切换真实对端。
//   - quic-go 看到的“逻辑对端地址”保持不变，因此 QUIC 层不需要重建连接，也尽量不感知对端变化。
//
// 说明：PoC 里也保留了 UDP proxy 的实现用于对比/替代，但主线结构是 wrapper 内部解耦。
//
// 本包职责：
//   - dial 初始 Target 并建立 QUIC 连接。
//   - 打开第一条双向 stream 作为控制流（newline JSON）。
//   - 监听 "migrate" 消息：
//       - 触发 MigrateSeen（供 APP 收紧 IO deadline/统计 downtime）
//       - 切换底层 UDP 真实对端（SwappableUDPConn.SetPeer）
//   - 保持 API 极简：业务 stream 与 IO 由 APP 自己掌控。
//
// quic-go API 使用说明（本项目只解释“我们怎么用”，不依赖库内部实现细节）：
//   - quic.DialAddr / quic.DialAddrEarly：基于 UDP 建立 QUIC session。
//   - Connection.OpenStreamSync：打开双向 stream。
//   - Stream 的 deadline（SetReadDeadline/SetWriteDeadline）：由 APP 用于
//     给“业务层”读写设置上限，从而度量迁移期间的真实中断时间。
package wrapper
