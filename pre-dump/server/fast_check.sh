#!/bin/bash
CONTAINER="vehicle-proxy"
PID=$(sudo podman inspect -f '{{.State.Pid}}' $CONTAINER)

echo "🚀 [Step 1] 发送信号 SIGUSR1 (闪断开始)..."
sudo kill -SIGUSR1 $PID

# 给 Go 程序 200ms 时间停止读写循环
sleep 0.2

echo "🧐 [Step 2] 检查容器状态..."
STATUS=$(sudo podman inspect -f '{{.State.Status}}' $CONTAINER)
echo "容器当前状态: $STATUS"

if [ "$STATUS" != "running" ]; then
    echo "❌ 错误: 容器已退出，信号处理逻辑可能有误！"
    sudo podman logs $CONTAINER | tail -n 5
    exit 1
fi

echo "📸 [Step 3] 触发 CRIU Checkpoint (关键时刻)..."
# 创建导出目录
sudo mkdir -p /tmp/checkpoint
# 执行快照
sudo podman container checkpoint $CONTAINER --export /tmp/checkpoint/final.tar.gz

if [ $? -eq 0 ]; then
    echo "🎉🎉🎉 成功！"
    echo "镜像已保存在: /tmp/checkpoint/final.tar.gz"
    echo "已经完成了 'QUIC 容器无感迁移' ！"
else
    echo "❌ 失败: CRIU 依然无法处理该进程状态。"
    echo "请查看错误日志: sudo podman logs $CONTAINER"
fi
