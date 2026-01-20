// Package wrapper 实现客户端侧的 QUIC “wrapper”。
//
// 设计目标（当前主线）：迁移对 QUIC 透明。
//
// 传统“重连式迁移”做法是：客户端收到 "migrate" 控制消息后，dial 到新地址/端口，
// 通过重建 QUIC session 完成切换。它很快（尤其配合 0-RTT），但 QUIC 并不透明。
//
// 本项目主要运行在“透明模式”下：
//   - 客户端始终连接到一个稳定端点（通常是 UDP proxy）。
//   - 服务实例可以从 A 迁移到 B，并在 CRIU restore 后进行 UDP rebind。
//   - proxy 只需更新后端目的地址并转发 UDP 包。
//   - 从客户端视角看，QUIC 的 target 不变（不需要切 target）。
//
// 本包职责：
//   - dial 到 Target 并建立 QUIC 连接。
//   - 打开第一条双向 stream 作为控制流（newline JSON）。
//   - 监听 "migrate" 消息，将 MigrateSeen 暴露给 APP。
//   - 保持 API 极简：业务 stream 与 IO 由 APP 自己掌控。
//
// quic-go API 使用说明（本项目只解释“我们怎么用”，不依赖库内部实现细节）：
//   - quic.DialAddr / quic.DialAddrEarly：基于 UDP 建立 QUIC session。
//   - Connection.OpenStreamSync：打开双向 stream。
//   - Stream 的 deadline（SetReadDeadline/SetWriteDeadline）：由 APP 用于
//     给“业务层”读写设置上限，从而度量迁移期间的真实中断时间。
package wrapper
