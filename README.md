# Wrapper

Wrapper 的目标是把“迁移编排（CRIU）”与“连接保持（QUIC 连接对迁移尽量透明）”做成一套可复用、可扩展的能力，用于在多台服务器之间迁移 Podman 容器内的应用实例。

本项目聚焦的核心是：
- server 侧：接收迁移指令并执行 dump/transfer/restore 的控制层能力。
- client 侧：在网络变更/迁移切换时，由 wrapper 在 UDP 层完成“对端切换”，尽量让 QUIC 不感知地址变化。

## 场景背景（简述）

目标场景为车路云协同的服务迁移：
- 每台服务器承载一个大模型（LLM）能力。
- 每个 Podman 容器内运行一个面向单车的业务 Agent。
- 车端通过 QUIC 与服务器通信。
- 随着车辆移动进入下一台服务器覆盖范围，需要触发迁移：将“源服务器某个容器内的业务 Agent”迁移到“目标服务器某个容器/壳环境”中，并让车端快速重连到新实例。

在该场景下的关键约束与目标：
- client 完全可控（可改代码/可嵌 SDK）。
- 允许服务中断，但目标中断时间尽量控制在 600ms 以内。
- 获取新地址方式：以服务端推送为主（migrate 指令），无需依赖客户端轮询。
- 迁移触发频率：约 20 分钟一次（非高频抖动迁移）。
- 应用一致性：迁移前后必须保持业务状态一致（需要严格的 quiesce/ack 语义）。

本仓库把系统解耦为：
- server 控制层（Control Layer）：Controller + Agent（同属控制面，可先合并部署）。
- server 数据层（Workload Layer）：容器/壳环境 + 可选的 runtime wrapper（提供 hook/ACK）。
- client 侧（Client Wrapper）：负责 QUIC 重连/切流与会话续接。

Server 控制层（同一侧）
Controller：接收迁移指令、做事务编排（prepare/commit/rollback）、更新映射（registry/DNS/LB）。
Agent：执行节点上的特权动作（podman/nsenter/criu、端口分配、传输、恢复、清理）。
这两者可以先做成一个进程的两个模块（MVP），以后再拆服务。
Server 数据层（业务/容器侧）
Runtime Wrapper（可选）：提供 freeze/resume/healthz 的本地契约，协助进入一致状态与恢复后初始化。
Client 侧
Client Wrapper（必需）：向 quic-go 提供稳定的 `net.PacketConn` 视图，并在迁移时切换真实 UDP 对端（peer swap），避免重建 QUIC 会话；同时提供业务层的恢复钩子与观测（downtime）。

<img width="1706" height="948" alt="架构" src="https://github.com/user-attachments/assets/805a3593-d409-4c7a-902e-04c29abd21c4" />


## 可靠运行（本机单次迁移链路）

前置依赖：
- Go 1.21（注意：系统里可能同时存在 Go 1.16/1.21，请优先使用 `/usr/local/go/bin/go`）
- Podman（需要 `sudo podman ...`）
- CRIU + nsenter（Control 会在容器外编排 dump/restore，并通过 nsenter 注入到壳容器）

一键跑通（Control 编排 + 统计中断时间）：
- `cd Wrapper`
- `/usr/local/go/bin/go build -o control ./Server/Control`
- `sudo ./control run --img-dir /dev/shm/criu-inject --criu-host-bin /usr/local/sbin/criu-4.1.1`

或使用脚本（推荐）：
- `./run.sh`（前台跑 client 并自动 up 容器）
- 另一个终端 `./migration.sh`（触发迁移并等待客户端打印 downtime）

预期输出：
- Control 依次打印“构建/启动/检查点/恢复/等待重连”等步骤
- Client 打印 ping/echo，并在切流后输出 `[客户端] 汇总：服务中断 xxxms`（目标尽量 < 600ms）
