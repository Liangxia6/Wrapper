FROM alpine:latest

# 替换为阿里云镜像源
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories

# 安装必要的运行时库 (Go 程序通常依赖 libc)
RUN apk add --no-cache libc6-compat

WORKDIR /app
COPY server_bin /app/server

# 暴露端口
EXPOSE 4242/udp

# 启动命令
CMD ["/app/server"]
