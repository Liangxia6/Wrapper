# Wrapper

Wrapper 的目标是把“迁移编排（CRIU）”与“连接保持（QUIC 连接对迁移尽量透明）”做成一套可复用、可扩展的能力，用于在多台服务器之间迁移 Podman 容器内的应用实例。
在模拟实验中，迁移时服务中断时间约300ms。

- 更详细内容见README2.md

## 场景背景（简述）

目标场景为车路云协同的服务迁移：
- 每台服务器承载一个大模型（LLM）能力。
- 每个 Podman 容器内运行一个面向单车的业务 Agent。
- 车端通过 QUIC 与服务器通信。
- 随着车辆移动进入下一台服务器覆盖范围，需要触发迁移：将“源服务器某个容器内的业务 Agent”迁移到“目标服务器的容器”中，并让车端快速重连到新实例。


本仓库把系统解耦为：
- server 控制层
- server 应用层
- client 侧
- 更详细内容见README2.md

该架构图为下一代Wrapper设计图，在此版本基础上加入Agent（现为podman内APP）与LLM（将部署在Server宿主机中）的通信管理

<img width="1706" height="948" alt="架构" src="https://github.com/user-attachments/assets/805a3593-d409-4c7a-902e-04c29abd21c4" />


## 实验结果
<img width="480" height="697" alt="TRACE 014544 255 4249） se1s10n" src="https://github.com/user-attachments/assets/661037f9-c2dc-48e9-bd74-caed0f5bfd73" />

