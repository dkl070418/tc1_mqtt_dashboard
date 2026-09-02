#!/usr/bin/env bash
# tc1_mqtt 一键初始化脚本
# 用法: bash init.sh   （在项目根目录下运行）
# 功能: 创建必要目录、生成空的 passwd/acl 文件、拉取镜像并启动全部服务

set -euo pipefail

# 获取脚本所在目录（项目根）
cd "$(dirname "$0")"

echo "==> 创建目录结构..."
mkdir -p mosquitto/config mosquitto/data mosquitto/log backend/data

echo "==> 初始化 mosquitto passwd / acl 文件..."
# 注意: 文件必须"非空"，否则 Docker bind-mount 会把空文件当成目录，
#       导致 backend 无法以文件方式打开 /data/passwd 和 /data/acl。
#       写入一个换行占位即可，Web 管理界面会接管后续内容。
if [ ! -f mosquitto/config/passwd ]; then
  echo "" > mosquitto/config/passwd
fi
if [ ! -f mosquitto/config/acl ]; then
  echo "" > mosquitto/config/acl
fi
chmod 600 mosquitto/config/passwd 2>/dev/null || true
chmod 600 mosquitto/config/acl    2>/dev/null || true

if [ ! -f mosquitto/config/mosquitto.conf ]; then
  echo "!! 缺少 mosquitto/config/mosquitto.conf，请参考 README 创建"
  exit 1
fi

if [ ! -f backend/config.yaml ]; then
  echo "!! 缺少 backend/config.yaml，请参考 README 添加（务必修改 admin 密码）"
  exit 1
fi

echo "==> 拉取镜像并启动服务 (docker compose pull && up -d)..."
docker compose pull
docker compose up -d

echo ""
echo "✓ 部署完成"
echo "  控制台管理页 : http://<本机IP>:80/"
echo "  注册页       : http://<本机IP>:80/register"
echo "  默认管理员   : 见 backend/config.yaml 的 admin_username/admin_password"
echo "  !! 生产环境请务必修改 backend/config.yaml 中的 admin 密码和 MQTT 账号后再使用"
