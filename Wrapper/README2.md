
# README2：Wrapper 各模块职责 & 迁移全流程（无 Proxy，完全由两端 wrapper 透明化）

本文档回答两个问题：
1) Wrapper 工程里“每一部分要做什么”，以及它们如何协作；
2) 一次完整的“外部触发迁移（CRIU 注入式迁移）”从开始到恢复的时序流程。

> 关键词：**不重建 QUIC 连接**、**地址变化对 QUIC 尽量透明**、**迁移中断时间以客户端观测为准**。

---

## 0. 目标与非目标

### 目标

- 迁移发生时：尽量让 quic-go 认为“连接一直存在”，避免重建 QUIC session。
- 对客户端业务：可容忍短暂停顿，但要把中断控制在较小窗口，并能清晰观测/量化（last echo → first echo after）。
- 对服务端恢复：CRIU restore 后必须解决“UDP socket 在新命名空间/端口映射下失效”的问题。


---

## 1. 总体架构（谁做什么）

可以把系统拆成三层：

1) **外部编排层（Control / 脚本）**：负责 podman/criu/nsenter 等特权动作。
2) **容器内服务进程（MEC Server Process）**：包含业务逻辑、QUIC 协议栈，以及 server wrapper。
3) **客户端进程（Car Client）**：包含业务逻辑、QUIC 协议栈，以及 client wrapper。

核心思想：

- **CRIU 负责搬运“进程内存态/寄存器态/文件句柄等”**。
- 但 UDP socket 在 restore 后不一定还能继续用：
	- 服务端：需要在容器 B 中 **rebind 本地 UDP**（新 socket）。
	- 客户端：需要在迁移发生后 **切换对端地址**，但**不让 QUIC 感知到对端变化**。

---

## 2. 仓库结构与每部分任务

以下路径均相对 `Wrapper/`。

### 2.1 Client/APP（客户端业务 Demo）

目录：`Client/APP`

职责（任务）：

- 作为“车端业务”的最小可运行版本：循环发送 Ping，读取 Echo。
- 通过 per-IO deadline（读写超时）度量中断时间。
- 监听 wrapper 暴露的 `MigrateSeen` 信号：
	- 将迁移后阶段的 IO timeout/interval 收紧（更快观测恢复）。
	- 记录 downtime：最后一次成功 echo 的时间 → 迁移后首次成功 echo 的时间。
- `-stay-connected` 模式：IO 出错时不结束 session，而是重新开 stream 并继续尝试。
	- 目的：迁移期间允许短暂 read deadline exceeded，但不主动退出，配合“连接不重建”的目标。

协作点：

- 从环境变量 `TARGET_ADDR` 获取初始服务端地址（host 上的 UDP 暴露端口，例如 `127.0.0.1:5242`）。
- 从环境变量 `TRANSPARENT` 自动开启 `stay-connected`（脚本已默认设置）。

### 2.2 Client/cWrapper（客户端 wrapper：透明对端切换）

目录：`Client/cWrapper`

职责（任务）：

- 建立 QUIC 连接（dial），并创建第一条双向 stream 作为**控制流**。
- 控制流协议：newline-delimited JSON（`hello` / `migrate` / `ack`）。
- 收到 `migrate(new ip:port)` 时：
	1) 触发 `MigrateSeen`（一次性 close channel）；
	2) **切换真实 UDP 对端**（peer swap）；
	3) 发送 `ack` 回服务端。

关键机制：**SwappableUDPConn（client 侧）**

- 目标：让 quic-go “看到”的 UDP 端点稳定（fake peer 不变），但真实发包对端可切换（real peer 可变）。
- 行为要点：
	- `WriteTo`：忽略 quic-go 传入的 addr，总是发往 `realPeer`。
	- `ReadFrom`：仅接收来自当前 `realPeer` 的包，并把来源地址伪装为 `fakePeer` 返回给 quic-go。
- 结果：迁移后即使服务端地址/端口变化，quic-go 层面尽量不触发“对端变化”的路径分支，从而避免重建 session。

### 2.3 Server/APP（服务端业务 Demo）

目录：`Server/APP`

职责（任务）：

- 作为“被迁移的业务进程”的最小可运行版本：对业务 stream 做 echo。
- 不处理迁移与 QUIC 细节（全部交给 sWrapper）。

### 2.4 Server/sWrapper（服务端 wrapper：可迁移 UDP + 迁移控制流）

目录：`Server/sWrapper`

职责（任务）：

- 在容器内监听 UDP（默认 `:4242`），在其上建立 QUIC listener。
- 约定：每个 QUIC 连接的第一条 stream 为控制流，用于迁移消息与 ACK。
- 信号集成点：
	- `SIGTERM`：触发向已连接客户端广播 `migrate` 并等待 `ack`（PoC 用于与外部 Control 协作）。
	- `SIGUSR2`：触发 UDP rebind（CRIU restore 后，socket 需要重建）。

关键机制：**MigratableUDP（server 侧）**

- 目标：CRIU restore 后能安全重建本地 UDP socket，但不让 quic-go 在并发读写时“因为旧 socket close 而崩”。
- 行为要点：
	- rebind 时：先创建新 socket，再 swap，再 close 旧 socket。
	- `ReadFrom/WriteTo`：若读写过程中遇到 `use of closed network connection` 且检测到 generation 已变化，则自动重试。

### 2.5 Server/Control（外部控制面：编排 podman/criu/nsenter）

目录：`Server/Control`

职责（任务）：

- **构建与启动**：编译 server/client，构建镜像，启动 A(源) 与 B(壳) 两个容器，并做端口映射：
	- host `SRC_PORT` → A 容器 `4242/udp`
	- host `DST_PORT` → B 容器 `4242/udp`
- **触发迁移**：
	- 给 A 中的 server 进程发 `SIGTERM`，让它广播 migrate 并等待客户端 ack。
- **CRIU 增量预拷贝（可选）**：多轮 `pre-dump --leave-running --track-mem`，最后 `dump --prev-images-dir`。
- **注入式恢复**：
	- kill A
	- `nsenter` 到 B 的命名空间内执行 `criu restore`
	- restore 后给恢复进程发 `SIGUSR2` 触发 UDP rebind

协作点：

- 与 sWrapper 的协作：通过 `SIGTERM`（migrate 广播）与 `SIGUSR2`（rebind）。
- 与 cWrapper 的协作：通过控制流消息 `migrate`/`ack` 形成“迁移事件的同步点”。

### 2.6 顶层脚本（推荐入口）

- `run.sh`：
	- `control up` 启动 A/B；
	- 启动 client（默认开启透明模式：`TRANSPARENT=1` 且 `-stay-connected`）。
- `migration.sh`：执行 `control migrate` 触发一次迁移，然后从 `client.out` 里等待“服务中断 xxxms”。
- `stop.sh`：停止 client，并调用 `control down` 清理 A/B 与镜像目录。
- `cleanup.sh`：更强力的清理（直接 rm 容器、rm 镜像目录、prune 悬空镜像）。

---

## 3. 两端 wrapper 如何协作（关键协议/信号/状态）

### 3.1 控制流协议（QUIC 第 1 条 stream）

- `hello`：client→server，标识 client（当前 PoC 主要用于日志/扩展点）。
- `migrate`：server→client，包含新地址/端口：`newAddr` + `newPort`。
- `ack`：client→server，确认已观测到 migrate 事件。

重要语义：

- `ack` **不等于**“业务已完全恢复”。它只表示 client 的控制面已经收到 migrate，并做了本地状态切换（包括 peer swap）。
- downtime 的统计以业务层（Client/APP）观测为准。

### 3.2 信号协作（容器外 Control ↔ 容器内 sWrapper）

- `SIGTERM`（发给 A 中 server 进程）：
	- sWrapper 捕获后向所有已连接客户端广播 `migrate`，并等待 ack（带超时）。
	- 目的是让“迁移事件”尽可能早地被客户端感知并切换 peer。
- `SIGUSR2`（发给 B 中 restore 后的 server 进程）：
	- sWrapper 捕获后执行 UDP rebind（MigratableUDP.Rebind）。
	- 目的是在新网络命名空间/端口映射下恢复收包能力。

---

## 4. 一次完整迁移流程（端到端时序）

以下描述的是当前 PoC 的典型时序（`./run.sh` + `./migration.sh` 或 `control run` 模式）。

### 4.1 稳态阶段（迁移前）

1) 外部 `control up` 启动：
	 - A（源容器）运行 `Server/APP`（内部由 sWrapper 提供 QUIC listener）。
	 - B（壳容器）仅占位（sleep infinity），用于后续注入 restore。
2) client 启动并连接 `TARGET_ADDR=127.0.0.1:SRC_PORT`。
3) client 与 A 建立 QUIC session：
	 - cWrapper 创建控制流（第 1 条 stream），发送 `hello`。
	 - 业务流（后续 stream）持续 ping/echo。

### 4.2 迁移触发阶段（让客户端先“看到迁移”）

4) 外部 `control migrate` 触发迁移（关键第一步）：
	 - 给 A 中 server 进程发 `SIGTERM`。
5) A 内 sWrapper 收到 `SIGTERM`：
	 - 生成迁移 id；
	 - 向所有连接的 client 发送 `migrate(id, newAddr, newPort)`；
	 - 等待 client 的 `ack`（带超时）。
6) client 的 cWrapper 收到 `migrate`：
	 - 触发 `MigrateSeen`（业务层可进入迁移态/收紧 timeout）；
	 - 调用 `SwappableUDPConn.SetPeer(newPeer)`，把真实 UDP 对端切到 `127.0.0.1:DST_PORT`；
	 - 发送 `ack`。

此时：客户端已经知道“接下来对端会变”，并且已经把底层 UDP 真实对端切到新地址，但 QUIC 连接本身仍保持。

### 4.3 CRIU 阶段（pre-dump/dump/restore）

7) （可选）pre-dump：
	 - 多轮 `criu pre-dump --leave-running --track-mem`，降低 final dump 体积。
8) final dump：
	 - `criu dump`（若有 pre-dump，则 `--prev-images-dir` 走增量链）。
9) kill A：
	 - 外部快速停止 A，避免双活。
10) restore 注入到 B：
	 - `nsenter` 到 B 的命名空间内执行 `criu restore --restore-detached`。
11) restore 后 rebind：
	 - 外部给恢复出来的进程发 `SIGUSR2`。
	 - B 内 sWrapper 收到 `SIGUSR2`，触发 `MigratableUDP.Rebind()` 重建本地 UDP socket。

### 4.4 恢复阶段（业务恢复与 downtime 统计）

12) client 业务流继续尝试：
	 - 迁移期间可能出现短暂 `read deadline exceeded`（网络停顿/对端不可达/服务端尚未 rebind）。
	 - `TRANSPARENT=1` 会启用 `-stay-connected`：client 不因短暂 IO 超时结束 session，而是重新开 stream 继续发包。
13) 当 B 侧服务恢复并能回 echo：
	 - Client/APP 捕获到“迁移后的第一条 echo”，输出：`[客户端] 汇总：服务中断 xxxms`。

---

## 5. 深度价值：它如何提升体验/性能（直观解释）

对比两种迁移方式：

### 5.1 没有 wrapper 内部透明（重连式迁移）

1) 迁移后 socket/路径变化导致 QUIC 出错 → session 关闭。
2) 客户端重新 dial/握手（至少 1-RTT；即使 0-RTT 也会有恢复成本）。
3) 连接重建会触发拥塞控制重新慢启动（CWND 变小），吞吐/时延都抖。

### 5.2 有 wrapper 内部透明（本项目主线）

1) 迁移期间：业务可能短暂停顿，但 QUIC session 尽量不被关闭。
2) client 侧仅切换底层 UDP 对端（peer swap），server 侧仅 rebind 本地 UDP socket。
3) QUIC 层更可能保持现有状态（包括拥塞窗口/路径状态），恢复后可更快回到稳定发送。

> 当前 PoC 的“收益形式”主要体现在：减少握手/重建成本与慢启动惩罚，让恢复更像“短暂停顿后继续”。

### 5.3 QUIC/CID 视角：为什么“换 IP 仍像同一连接”（直觉版）

你可以把 QUIC 的“连接身份”理解成：**Connection ID（CID）** + 会话密钥 + 状态机。

- QUIC 包里有 CID（更准确地说是 QUIC 包头的 Destination Connection ID 等字段），用于让接收端在用户态快速把包分流到正确的连接。
- CRIU 的作用：把 MEC-A 上 server 进程的**用户态内存**完整搬到 MEC-B（包含 QUIC 状态机、密钥、以及 CID 等）。
- wrapper 的作用：
	- 服务端：restore 后旧 UDP socket 可能不可用，sWrapper 用 MigratableUDP 在不“惊扰”quic-go 的前提下 rebind 到新 socket。
	- 客户端：迁移后服务端的可达地址/端口变化，cWrapper 用 SwappableUDPConn 把真实对端悄悄切到新地址，但对 quic-go 伪装成“逻辑对端不变”。

于是迁移后会发生一个看起来“很魔法”的现象：

- 客户端收到一个来自新 IP/端口的 QUIC 包。
- 客户端用包头里的 CID 做 demux，发现它对应的就是“旧连接”，所以把包交给同一个 QUIC session。
- 对客户端来说：“他只是换了手机号（IP/端口），但 CID/密钥没变，所以还是同一个人”。

对应到 wrapper 的操作序列（更工程化一点）：

- 正常时：wrapper 把读写转交给当前 socket/对端。
- 迁移时：
	- server：MigratableUDP 关闭旧 socket 并 rebind 新 socket（接口对 quic-go 保持稳定）。
	- client：SwappableUDPConn 把 real peer 从 A 切到 B（接口对 quic-go 保持稳定）。
- QUIC 的反应：quic-go 继续调用同一个 `PacketConn.ReadFrom/WriteTo`，并认为调用成功；连接状态（含 CID）仍在，因此尽量不会走“重建连接”的路径。

> 说明：标准 QUIC 还可能涉及路径验证/反放大等机制；本 PoC 的目标是验证“CID+内存态保留 + wrapper 提供稳定 socket 视图”在迁移场景下能实现尽量透明的恢复。

---

## 6. 如何运行（推荐）

前置依赖：Go 1.21、podman、criu、nsenter、sudo 可用。

一键启动（前台跑 client，并准备 A/B）：

- `cd Wrapper`
- `./run.sh`

触发一次迁移（另一个终端）：

- `cd Wrapper`
- `./migration.sh`

停止并清理：

- `./stop.sh`

---

## 7. 未来扩展点（可选）

- 客户端本地地址变化：可基于 `SwappableUDPConn.RebindLocal()` 支持本地 UDP 重绑（例如车端换网卡/IP）。
- 更严格的一致性：把 `ack` 从“已观测迁移”升级为“业务已 quiesce/可安全 dump”的双阶段协议。
- 更真实的多机迁移：加入镜像传输、registry/DNS/LB 更新与安全控制。

