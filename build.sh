#!/usr/bin/env bash
# 构建并推送 amd64 + arm64 双架构镜像到阿里云镜像仓库
# 用法:
#   ./build.sh            # tag=latest
#   ./build.sh v1.0.0     # 自定义 tag

set -euo pipefail

REGISTRY="crpi-wjscuxkxm163odo3.cn-hangzhou.personal.cr.aliyuncs.com/dkl091219"
TAG="${1:-latest}"
PLATFORMS="linux/amd64,linux/arm64"
BUILDER="mqtt-mgr-builder"

echo "==> 登录阿里云镜像仓库（个人版）..."
docker login crpi-wjscuxkxm163odo3.cn-hangzhou.personal.cr.aliyuncs.com

echo "==> 初始化 buildx builder: ${BUILDER}"
docker buildx inspect "${BUILDER}" &>/dev/null \
  || docker buildx create --name "${BUILDER}" --driver docker-container --bootstrap
docker buildx use "${BUILDER}"

echo "==> 构建并推送 backend  [${PLATFORMS}]"
docker buildx build \
  --platform "${PLATFORMS}" \
  --tag "${REGISTRY}/mqtt-mgr-backend:${TAG}" \
  --push \
  ./backend

echo "==> 构建并推送 nginx(frontend)  [${PLATFORMS}]"
docker buildx build \
  --platform "${PLATFORMS}" \
  --tag "${REGISTRY}/mqtt-mgr-nginx:${TAG}" \
  -f ./frontend/Dockerfile \
  --push \
  .

# ------------------------------------------------------------------
# 构建后自校验：拉取刚推送的镜像，抽查关键内容，防止 docker 缓存带出旧构建
# 任一项不通过即终止（set -e），避免把"旧货"误当成功发布。
# ------------------------------------------------------------------
verify_backend() {
  local img="$1" cid tmp
  echo ""
  echo "==> 校验 backend 镜像内容 ($TAG) ..."
  tmp="$(mktemp -d)"
  cid="$(docker create --platform linux/amd64 "${img}")"
  docker cp "${cid}":/app/mqtt_mgr "${tmp}/mqtt_mgr" >/dev/null 2>&1
  docker rm -f "${cid}" >/dev/null 2>&1

  local need=("power/query" "power/summary" "power_samples" "power_agg_1min" "power_agg_1h" "power_stats.db" "device/ztc1/%s/sensor")
  local fail=0
  for s in "${need[@]}"; do
    if strings "${tmp}/mqtt_mgr" | grep -qF -- "${s}"; then
      echo "  [OK]   backend 含: ${s}"
    else
      echo "  [FAIL] backend 缺少: ${s}  (可能走了旧缓存!)"
      fail=1
    fi
  done
  rm -rf "${tmp}"
  if [ "${fail}" -ne 0 ]; then
    echo "!! backend 校验失败，请检查构建缓存，终止发布"
    exit 1
  fi
}

verify_nginx() {
  local img="$1" cid tmp appjs
  echo ""
  echo "==> 校验 nginx 前端内容 ($TAG) ..."
  tmp="$(mktemp -d)"
  cid="$(docker create --platform linux/amd64 "${img}")"
  docker cp "${cid}":/usr/share/nginx/html/assets "${tmp}/assets" >/dev/null 2>&1
  docker rm -f "${cid}" >/dev/null 2>&1
  appjs="$(ls ${tmp}/assets/App-*.js 2>/dev/null | head -1)"
  if [ -z "${appjs}" ]; then
    echo "!! 未找到前端 App chunk，终止发布"
    rm -rf "${tmp}"
    exit 1
  fi

  local fail=0
  # 1. 版本号
  if grep -qoF "${TAG}" "${appjs}"; then
    echo "  [OK]   前端版本号: ${TAG}"
  else
    echo "  [FAIL] 前端版本号缺失: ${TAG} (可能走了旧缓存!)"
    fail=1
  fi
  # 2. 水印关闭 (lightweight-charts v5 编译后 attributionLogo:!1)
  if grep -qoF 'attributionLogo:!1' "${appjs}"; then
    echo "  [OK]   TradingView 水印已关闭"
  else
    echo "  [WARN] 水印关闭标记未找到 (可接受，若未启用功耗图则无需关注)"
  fi
  # 3. 当前功率已移除 (不应出现)
  if grep -qF '当前功率' "${appjs}"; then
    echo "  [FAIL] 前端仍包含旧版'当前功率'文案 (旧缓存!)"
    fail=1
  else
    echo "  [OK]   '当前功率'已移除"
  fi
  # 4. 功耗面板核心元素
  for s in "今日用电" "本月用电" "power/query"; do
    if grep -qF -- "${s}" "${appjs}"; then
      echo "  [OK]   前端含: ${s}"
    else
      echo "  [WARN] 前端缺少: ${s} (如本期未涉及功耗功能可忽略)"
    fi
  done
  rm -rf "${tmp}"
  if [ "${fail}" -ne 0 ]; then
    echo "!! nginx 校验失败，请检查构建缓存，终止发布"
    exit 1
  fi
}

verify_backend "${REGISTRY}/mqtt-mgr-backend:${TAG}"
verify_nginx "${REGISTRY}/mqtt-mgr-nginx:${TAG}"

echo ""
echo "✓ 构建自校验全部通过"
echo "  ${REGISTRY}/mqtt-mgr-backend:${TAG}"
echo "  ${REGISTRY}/mqtt-mgr-nginx:${TAG}"

echo ""
echo "==> 拉取最新镜像并重启服务..."
docker-compose pull backend nginx
docker-compose up -d backend nginx
echo "✓ 服务已重启"
