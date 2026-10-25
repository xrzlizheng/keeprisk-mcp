#!/bin/bash
#
# KeepRisk MCP 热更新脚本
# 使用方法：bash hot-reload.sh
#
# 工作流程：
#   1. 拉取最新代码（可选）
#   2. 编译新的二进制文件
#   3. 发送 SIGHUP 信号给容器内的进程
#   4. 等待容器自动重启并验证

set -e

# 配置
CONTAINER_NAME="keeprisk-mcp-keeprisk-mcp-1"
BINARY_PATH="./build/keeprisk-mcp"
PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
SKIP_GIT_PULL=false

# 解析参数
while [[ $# -gt 0 ]]; do
    case $1 in
        --skip-pull)
            SKIP_GIT_PULL=true
            shift
            ;;
        --help|-h)
            echo "KeepRisk MCP 热更新脚本"
            echo ""
            echo "用法: bash hot-reload.sh [选项]"
            echo ""
            echo "选项:"
            echo "  --skip-pull    跳过 git pull（使用本地代码）"
            echo "  --help, -h     显示帮助信息"
            echo ""
            echo "示例:"
            echo "  bash hot-reload.sh              # 拉取代码 + 编译 + 热更新"
            echo "  bash hot-reload.sh --skip-pull  # 仅编译 + 热更新"
            exit 0
            ;;
        *)
            echo "未知参数: $1"
            echo "使用 --help 查看帮助"
            exit 1
            ;;
    esac
done

cd "$PROJECT_DIR"

echo "=========================================="
echo "  KeepRisk MCP 热更新"
echo "=========================================="

# 步骤 1：拉取代码
if [ "$SKIP_GIT_PULL" != "true" ]; then
    echo ""
    echo "[1/4] 拉取最新代码..."
    if git remote get-url origin 2>/dev/null; then
        git fetch origin master 2>/dev/null || true
        LATEST_COMMIT=$(git rev-parse origin/master 2>/dev/null || echo "")
        CURRENT_COMMIT=$(git rev-parse HEAD 2>/dev/null || echo "")
        
        if [ "$LATEST_COMMIT" != "" ] && [ "$LATEST_COMMIT" != "$CURRENT_COMMIT" ]; then
            echo "  检测到新提交，正在拉取..."
            git pull origin master
        else
            echo "  已是最新代码"
        fi
    else
        echo "  无远程仓库，跳过拉取"
    fi
else
    echo ""
    echo "[1/4] 跳过 git pull (--skip-pull)"
fi

# 步骤 2：编译二进制
echo ""
echo "[2/4] 编译二进制文件..."
mkdir -p build
CGO_ENABLED=0 GOOS=linux /usr/local/go/bin/go build -trimpath -ldflags="-s -w" -o "$BINARY_PATH" ./cmd/keeprisk-mcp

if [ -f "$BINARY_PATH" ]; then
    echo "  编译成功: $BINARY_PATH"
    ls -lh "$BINARY_PATH"
else
    echo "  编译失败！"
    exit 1
fi

# 步骤 3：发送 SIGHUP 信号
echo ""
echo "[3/4] 发送热更新信号..."

# 检查容器是否运行
if ! docker ps --filter name="$CONTAINER_NAME" --filter status=running | grep -q "$CONTAINER_NAME"; then
    echo "  容器未运行，使用正常启动方式..."
    docker compose up -d
    echo "  容器已启动"
else
    # 查找容器内的主进程并发送 SIGHUP
    # 在 distroless 容器中，主进程的 PID 通常是 1
    echo "  正在查找容器内主进程..."
    
    # 使用 docker exec 发送信号
    # distroless 没有 shell，使用 kill 命令直接发送
    if docker exec "$CONTAINER_NAME" kill -HUP 1 2>/dev/null; then
        echo "  SIGHUP 信号已发送到主进程 (PID 1)"
    else
        # 备选方案：直接 kill 容器进程
        echo "  尝试使用 docker kill 发送信号..."
        docker kill --signal=SIGHUP "$CONTAINER_NAME" 2>/dev/null || {
            echo "  信号发送失败，使用重启方式..."
            docker compose restart
            echo "  容器已重启"
        }
    fi
fi

# 步骤 3.5：校验容器内二进制与宿主一致
# 目录挂载（./build:/app/bin）下正常运行路径：SIGHUP → main.go exec /app/bin/keeprisk-mcp。
# 若 exec 未能切换（如容器由旧镜像创建、二进制无 /app/bin 探测逻辑），需重建镜像+容器
# （Dockerfile.local COPY build/keeprisk-mcp），确保容器内运行代码与宿主一致。
HOST_MD5=$(md5sum "$BINARY_PATH" | awk '{print $1}')
TMP_BIN="/tmp/.keeprisk_mcp_container_bin"
CONTAINER_MD5=""
for CPATH in /app/bin/keeprisk-mcp /keeprisk-mcp; do
    if docker cp "$CONTAINER_NAME:$CPATH" "$TMP_BIN" 2>/dev/null; then
        CONTAINER_MD5=$(md5sum "$TMP_BIN" | awk '{print $1}')
        echo "  校验对象: 容器内 $CPATH"
        break
    fi
done
rm -f "$TMP_BIN"
if [ -z "$CONTAINER_MD5" ]; then
    echo "  ⚠️ 无法读取容器内二进制（容器可能未运行），跳过一致性校验"
elif [ "$HOST_MD5" != "$CONTAINER_MD5" ]; then
    echo "  ⚠️ 容器内二进制与宿主不一致，重建镜像与容器以加载新代码..."
    docker compose up -d --build --force-recreate
    echo "  容器已重建"
else
    echo "  ✅ 容器内二进制与宿主一致"
fi

# 步骤 4：等待并验证
echo ""
echo "[4/4] 验证服务状态..."
echo "  等待服务启动..."

# 等待服务就绪（最多 30 秒）
MAX_RETRIES=15
RETRY_INTERVAL=2
READY=false

for i in $(seq 1 $MAX_RETRIES); do
    sleep "$RETRY_INTERVAL"
    if curl -s http://localhost:8080/health 2>/dev/null | grep -q '"status":"ok"'; then
        if curl -s http://localhost:8080/ready 2>/dev/null | grep -q '"ready":true'; then
            READY=true
            break
        fi
    fi
    echo "  等待中... ($i/$MAX_RETRIES)"
done

if [ "$READY" = "true" ]; then
    echo "  ✅ 服务已就绪！"
    echo ""
    echo "健康检查:"
    curl -s http://localhost:8080/health | python3 -m json.tool 2>/dev/null || curl -s http://localhost:8080/health
    echo ""
    echo "就绪状态:"
    curl -s http://localhost:8080/ready | python3 -m json.tool 2>/dev/null || curl -s http://localhost:8080/ready
else
    echo "  ⚠️  服务可能还在启动中，请稍后手动检查"
    echo "  健康检查: curl http://localhost:8080/health"
    echo "  就绪检查: curl http://localhost:8080/ready"
fi

echo ""
echo "=========================================="
echo "  热更新完成"
echo "=========================================="

# 显示容器状态
echo ""
echo "容器状态:"
docker ps --filter name="$CONTAINER_NAME" --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}" 2>/dev/null || echo "  容器状态未知"