#!/bin/bash

# 遇到错误立即退出
set -e

# 使用新安装的 Go
export PATH=/usr/local/go/bin:$PATH

# 设置 Go 代理为国内镜像
export GOPROXY=https://goproxy.cn,direct

# 定义清理函数
cleanup() {
    echo "🧹 Cleaning up..."
    if [ -n "$CLIENT_PID" ]; then
        kill $CLIENT_PID 2>/dev/null || true
    fi
    if [ -n "$TAIL_PID" ]; then
        kill $TAIL_PID 2>/dev/null || true
    fi
    # 确保杀死所有残留的 client_bin
    pkill -f client_bin || true
}
# 脚本退出时执行清理 (包括正常退出、Ctrl+C、错误退出)
trap cleanup EXIT

# 1. 编译 Server (静态链接，方便容器化)
echo "🔨 Compiling Server..."
# 确保依赖完整
go mod tidy 
cd server
CGO_ENABLED=0 GOOS=linux go build -o server_bin .
cd ..

# 编译 Client
echo "🔨 Compiling Client..."
cd client
go build -o client_bin .
cd ..

# 2. 构建容器镜像
echo "🐳 Building Docker Image..."
sudo podman build -t mec-server-criu ./server

# 3. 运行容器
echo "🚀 Starting Container..."
# --privileged: CRIU 需要特权
# --name mec-inst: 容器名
# -p 4242:4242: 端口映射
sudo podman run -d --privileged --name mec-inst -p 4242:4242/udp mec-server-criu > /dev/null

echo "✅ Server started. PID:"
PID=$(sudo podman inspect -f '{{.State.Pid}}' mec-inst)
echo $PID

# 4. 启动客户端
echo "🚗 Starting Client..."
touch client.log
# 使用 stdbuf 确保输出不被缓冲，同时输出到文件和屏幕
stdbuf -oL ./client/client_bin > client.log 2>&1 &
CLIENT_PID=$!

# 实时显示客户端日志 (后台运行)
sleep 0.5
tail -f client.log &
TAIL_PID=$!

# 等待数据流稳定
sleep 3

# ==========================================
# 阶段 1: Pre-dump (真实 CRIU 操作)
# ==========================================
echo "⚡️ [Phase 1] Pre-dump Start..."
START_TIME=$(date +%s%3N)

# 1. 通知应用关闭 Socket (为了让 CRIU 检查通过)
sudo kill -SIGUSR1 $PID
sleep 0.05 # 缩短等待时间，Go 关闭 Socket 很快

# 2. 执行 Checkpoint 但保持运行 (--leave-running)
# 优化：使用 /dev/shm (内存) 模拟不落盘，避免磁盘 I/O
# 优化：--compress=none 禁用压缩，节省 CPU 时间
echo "📸 Executing Podman Checkpoint (Leave Running)..."
sudo mkdir -p /dev/shm/checkpoint
sudo podman container checkpoint --leave-running --compress=none --export /dev/shm/checkpoint/predump.tar mec-inst > /dev/null

if [ $? -ne 0 ]; then
    echo "❌ Pre-dump Failed! Check logs."
    sudo podman logs mec-inst
    exit 1
fi

# 3. 通知应用恢复 Socket
sudo kill -SIGUSR2 $PID
END_TIME=$(date +%s%3N)
PREDUMP_DURATION=$((END_TIME - START_TIME))
echo "✅ [Phase 1] Pre-dump Done. Service Resumed. Duration: ${PREDUMP_DURATION}ms"

# 继续运行一段时间，验证客户端是否自动恢复
echo "⏳ Waiting for client to recover from Pre-dump (5s)..."
sleep 5

# ==========================================
# 阶段 2: Final Checkpoint (真实迁移)
# ==========================================
echo "⚡️ [Phase 2] Final Migration..."
START_TIME=$(date +%s%3N)

# 1. 通知应用发送迁移指令 (SIGTERM)
# 应用收到这个信号后，会广播指令，然后自己退出或等待被 Kill
sudo kill -SIGTERM $PID
sleep 0.05 # 缩短等待时间

# 2. 执行最终 Checkpoint (容器将停止)
# 优化：使用管道直接传输 (Pipe) 或内存盘
# 这里演示使用内存盘 /dev/shm，这是单机模拟"不落盘"的最佳实践
# 优化：--compress=none 禁用压缩
echo "📸 Executing Final Checkpoint..."
sudo podman container checkpoint --compress=none --export /dev/shm/checkpoint/final.tar mec-inst > /dev/null

if [ $? -eq 0 ]; then
    END_TIME=$(date +%s%3N)
    FINAL_DURATION=$((END_TIME - START_TIME))
    echo "🎉 Final Checkpoint Successful! Duration: ${FINAL_DURATION}ms"
else
    echo "❌ Final Checkpoint Failed!"
    exit 1
fi

# ==========================================
# 阶段 3: Restore (恢复服务)
# ==========================================
echo "⚡️ [Phase 3] Restore Service..."
START_TIME=$(date +%s%3N)

# 模拟迁移：删除旧容器
echo "🗑️ Removing old container..."
sudo podman rm -f mec-inst > /dev/null

# 恢复容器
echo "♻️ Restoring container from checkpoint..."
# --import: 从归档文件恢复
# --name: 给恢复的容器起个新名字
# -p: 必须重新指定端口映射，否则外部无法访问
sudo podman container restore --import /dev/shm/checkpoint/final.tar --name mec-inst-restored -p 4242:4242/udp > /dev/null

END_TIME=$(date +%s%3N)
RESTORE_DURATION=$((END_TIME - START_TIME))
echo "✅ Service Restored! Duration: ${RESTORE_DURATION}ms"

# 等待客户端重连
echo "⏳ Waiting for client to reconnect and exchange data (5s)..."
sleep 5

# 提取客户端重连时间 (从客户端日志中 grep)
# 假设客户端输出格式: "✅ Reconnected in 123ms"
CLIENT_RECONNECT_TIME=$(grep "Reconnected in" client.log | tail -n 1 | awk '{print $4}')

# 查看恢复后的容器日志
echo "📜 Restored Container Logs:"
sudo podman logs mec-inst-restored

# ==========================================
# 📊 Final Performance Report
# ==========================================
TOTAL_MIGRATION_TIME=$((PREDUMP_DURATION + FINAL_DURATION + RESTORE_DURATION))

echo ""
echo "=========================================="
echo "       🚀 MIGRATION PERFORMANCE REPORT    "
echo "=========================================="
echo "1️⃣  Pre-dump Duration      : ${PREDUMP_DURATION} ms"
echo "2️⃣  Final Checkpoint Time  : ${FINAL_DURATION} ms"
echo "3️⃣  Restore Duration       : ${RESTORE_DURATION} ms"
echo "------------------------------------------"
echo "⏱️  Total Downtime (Est.)  : $((FINAL_DURATION + RESTORE_DURATION)) ms"
echo "⏱️  Total Migration Time   : ${TOTAL_MIGRATION_TIME} ms"
if [ ! -z "$CLIENT_RECONNECT_TIME" ]; then
echo "🔄 UDP Reconnect Time     : ${CLIENT_RECONNECT_TIME}"
else
echo "🔄 UDP Reconnect Time     : N/A (Check client logs)"
echo "--- Client Log Dump ---"
cat client.log
echo "-----------------------"
fi
echo "=========================================="
echo ""

# 保持脚本运行一会，让用户看到输出
sleep 2

# ... (清理部分) ...
kill $CLIENT_PID
