# 部署指南（运行手册）

> 最后整理：2026-11-24 · 覆盖"构建 → 推阿里云 ACR → 切换上线 → 验证 → 回滚"
> 这份文档替代早先的零散记录；命令都按当前生产环境（NAS 192.168.1.10 + dpanel）校对过。

---

## 0. 一页速查

```bash
# ── 在 NAS 上（deploy 用户，已 docker login）
cd /tmp/build_<版本缩写>                     # 源码目录（见 §2 同步方式）

# 1) 构建（单架构，本地 daemon；基础镜像先走国内镜像源）
docker build -t $R/mqtt-mgr-backend:vX.Y.Z --build-arg GOPROXY=https://goproxy.cn,direct ./backend
docker build -t $R/mqtt-mgr-nginx:vX.Y.Z   --build-arg NPM_REGISTRY=https://registry.npmmirror.com -f frontend/Dockerfile .

# 2) 推送
docker push $R/mqtt-mgr-backend:vX.Y.Z
docker push $R/mqtt-mgr-nginx:vX.Y.Z

# 3) 切换（compose 由 dpanel 管理，注意 root 权限与 .env）
docker exec dpanel sh -c "cp /dpanel/compose/mqtt/docker-compose.yaml /dpanel/compose/mqtt/docker-compose.yaml.bak_vXYZ && \
  sed -i 's|mqtt-mgr-backend:v旧|mqtt-mgr-backend:vX.Y.Z|; s|mqtt-mgr-nginx:v旧|mqtt-mgr-nginx:vX.Y.Z|' /dpanel/compose/mqtt/docker-compose.yaml"
docker compose -p mqtt --env-file /dev/null -f /opt/dpanel/compose/mqtt/docker-compose.yaml up -d backend nginx

# 4) 验证（见 §5）
```

其中 `$R=crpi-wjscuxkxm163odo3.cn-hangzhou.personal.cr.aliyuncs.com/dkl091219`。

---

## 1. 环境信息

| 项 | 值 |
|---|---|
| NAS | `192.168.1.10`（ubuntu, amd64/x86_64） |
| SSH | `ssh deploy@192.168.1.10`（本机已有免密；DEPLOY 旧文档里的 `id_ed25519_tc1` 已不存在） |
| 应用地址 | http://192.168.1.10:8071/ |
| 运行栈管理 | **dpanel**（不是手写 compose 目录）：compose 文件 `/opt/dpanel/compose/mqtt/docker-compose.yaml`（root:root 600 同级 `.env`） |
| docker 登录用户 | `dkl091219`（凭据在 NAS `~/.docker/config.json`，已验证） |

---

## 2. 阿里云镜像仓库（ACR 个人版）

| 项 | 值 |
|---|---|
| Registry（公网） | `crpi-wjscuxkxm163odo3.cn-hangzhou.personal.cr.aliyuncs.com` |
| Registry（VPC 内网，NAS/ECS 同在阿里云 VPC 时用） | `crpi-wjscuxkxm163odo3-vpc.cn-hangzhou.personal.cr.aliyuncs.com` |
| 命名空间 | `dkl091219` |
| 镜像 ①（后端） | `…/dkl091219/mqtt-mgr-backend:<tag>` |
| 镜像 ②（前端/nginx） | `…/dkl091219/mqtt-mgr-nginx:<tag>` |

### 2.1 登录

```bash
docker login --username=dkl091219 crpi-wjscuxkxm163odo3.cn-hangzhou.personal.cr.aliyuncs.com
```

用户名是阿里云账号全名，密码在 ACR 控制台「访问凭证」页设置/重置。
（`build.sh` 里写的是不带 `--username` 的形式，已登录过也能用；两者等价。）

### 2.2 推任意已有镜像（不重新构建的场合）

```bash
docker pull  crpi-wjscuxkxm163odo3.cn-hangzhou.personal.cr.aliyuncs.com/dkl091219/mqtt-mgr-backend:v2.1.7
docker tag   <ImageId> crpi-wjscuxkxm163odo3.cn-hangzhou.personal.cr.aliyuncs.com/dkl091219/mqtt-mgr-backend:v2.1.8
docker push  crpi-wjscuxkxm163odo3.cn-hangzhou.personal.cr.aliyuncs.com/dkl091219/mqtt-mgr-backend:v2.1.8
```

前端同理，把 `mqtt-mgr-backend` 换成 `mqtt-mgr-nginx`。

---

## 3. 构建

### 3.1 有外网的 Linux 机器：`build.sh`（多架构 + 自校验）

```bash
./build.sh            # tag=latest
./build.sh v2.1.7     # 自定义 tag
```

- 构建 `linux/amd64,linux/arm64` 并 `--push` 两个镜像；
- 构建后**自动拉回镜像自校验**（backend 关键字符串、前端版本号/水印/旧文案），防止 docker 缓存带出旧产物；
- 最后 `docker-compose pull && up -d`（**注意：这一段依赖当前目录的 `docker-compose.yaml`，与 dpanel 管理的生产栈不是同一份，生产上请按 §4 手动切**）。

⚠️ 在 NAS 上跑 `build.sh` 会失败：buildx 的 buildkit 容器拉不到镜像（见 §7）。

### 3.2 NAS（墙内）：本地 daemon 单架构构建

```bash
# 基础镜像走国内镜像源，再 tag 回原名（Dockerfile 保持原样）
docker pull docker.1panel.live/library/golang:1.27.3-alpine && docker tag docker.1panel.live/library/golang:1.27.3-alpine golang:1.27.3-alpine
docker pull docker.1panel.live/library/node:24.22.1-alpine   && docker tag docker.1panel.live/library/node:24.22.1-alpine   node:24.22.1-alpine
docker pull docker.1panel.live/library/nginx:alpine          && docker tag docker.1panel.live/library/nginx:alpine          nginx:alpine
docker pull docker.1panel.live/library/alpine:latest         && docker tag docker.1panel.live/library/alpine:latest         alpine:latest

R=crpi-wjscuxkxm163odo3.cn-hangzhou.personal.cr.aliyuncs.com/dkl091219
docker build -t $R/mqtt-mgr-backend:vX.Y.Z --build-arg GOPROXY=https://goproxy.cn,direct ./backend
docker build -t $R/mqtt-mgr-nginx:vX.Y.Z   --build-arg NPM_REGISTRY=https://registry.npmmirror.com -f frontend/Dockerfile .
docker push $R/mqtt-mgr-backend:vX.Y.Z
docker push $R/mqtt-mgr-nginx:vX.Y.Z
```

### 3.3 代码怎么同步到 NAS

本机没有可用的 TLS 链路（`git push` / `curl https://github.com` 都报 `schannel: AcquireCredentialsHandle failed`），
所以用打包 + scp：

```powershell
# 本机（PowerShell），只带构建需要的目录
tar -czf .tmpbuild/tc1-src.tar.gz --exclude=.git --exclude=.tmpbuild --exclude=node_modules --exclude=backend/mqtt_mgr.exe backend frontend nginx docker-compose.yaml README.md CHANGELOG.md docs
scp .tmpbuild/tc1-src.tar.gz deploy@192.168.1.10:/tmp/tc1-src.tar.gz
```

```bash
# NAS
rm -rf /tmp/build_<版本> && mkdir -p /tmp/build_<版本>
tar xzf /tmp/tc1-src.tar.gz -C /tmp/build_<版本>
```

---

## 4. 切换上线（dpanel 管理的 compose）

```bash
COMPOSE=/opt/dpanel/compose/mqtt/docker-compose.yaml

# 4.1 改 tag（文件是 root 所有，借 dpanel 容器内的 root 改；先备份）
docker exec dpanel sh -c "cp /dpanel/compose/mqtt/docker-compose.yaml /dpanel/compose/mqtt/docker-compose.yaml.bak_vXYZ && \
  sed -i 's|mqtt-mgr-backend:v旧|mqtt-mgr-backend:vX.Y.Z|; s|mqtt-mgr-nginx:v旧|mqtt-mgr-nginx:vX.Y.Z|' /dpanel/compose/mqtt/docker-compose.yaml && \
  grep -n 'image:.*mqtt-mgr' /dpanel/compose/mqtt/docker-compose.yaml"

# 4.2 重建（必须 --env-file /dev/null，否则 .env 权限报错）
docker compose -p mqtt --env-file /dev/null -f $COMPOSE up -d backend nginx
```

固件/数据库相关的挂载路径不变：`config.yaml`、`metadata/`、`power/`、`mosquitto_config` 卷。

---

## 5. 上线验证清单

```bash
docker ps --filter name=mqtt-mgr --format '{{.Names}}\t{{.Image}}\t{{.Status}}'   # 两个都应是新 tag，Up
docker logs mqtt-mgr-backend 2>&1 | grep -E '\[scheduler\]|\[timesync\]|服务启动' | tail -5
curl -s -o /dev/null -w "ping=%{http_code}\n" http://127.0.0.1:8071/api/ping       # 200
docker exec mqtt-mgr-nginx sh -c "grep -o 'vX\.Y\.Z' /usr/share/nginx/html/assets/App-*.js | head -1"  # 前端版本号
```

可选（管理员态）：

```bash
CFG=/srv/tc1/config.yaml
U=$(awk -F': *' '/^admin_username:/{print $2}' $CFG | tr -d '"')
P=$(awk -F': *' '/^admin_password:/{print $2}' $CFG | tr -d '"')
TOKEN=$(curl -s -X POST http://127.0.0.1:8071/api/admin/login -H 'Content-Type: application/json' \
  -d "{\"username\":\"$U\",\"password\":\"$P\"}" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("token",""))')
curl -s -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8071/api/admin/config
curl -s -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8071/api/admin/diagnostics/time-sync
```

---

## 6. 回滚

```bash
# 6.1 镜像级：把 compose tag 改回上一个版本并重建
docker exec dpanel sh -c "sed -i 's|mqtt-mgr-backend:vX.Y.Z|mqtt-mgr-backend:v旧|; s|mqtt-mgr-nginx:vX.Y.Z|mqtt-mgr-nginx:v旧|' /dpanel/compose/mqtt/docker-compose.yaml"
docker compose -p mqtt --env-file /dev/null -f /opt/dpanel/compose/mqtt/docker-compose.yaml up -d backend nginx

# 6.2 配置级：恢复备份的 compose / config.yaml
ls /opt/dpanel/compose/mqtt/docker-compose.yaml.bak_*
ls /srv/tc1/config.yaml.bak_*
```

---

## 7. 注意事项 / 已知坑

1. **`build.sh` 在 NAS 上不可用**：buildx 的 buildkit 容器拉不到基础镜像（墙），必须用 §3.2 的本地 daemon 单架构构建；NAS 是 amd64，单架构够用。
2. **compose 归 dpanel 管**：`/opt/dpanel/compose/mqtt/` 里 `docker-compose.yaml` 与 `.env` 都是 root:root，`sudo` 需要密码 →
   改文件借 `docker exec dpanel`，跑 compose 一律加 `--env-file /dev/null`（文件里没有变量插值，绕过是安全的）。
3. **别用 `docker-compose`（v1）**，NAS 上是 `docker compose` v2.35。
4. 基础镜像版本要和 Dockerfile 对齐（当前 `golang:1.27.3-alpine`、`node:24.22.1-alpine`、`nginx:alpine`），
   只拉 `:latest` 会悄悄换编译器/Node 版本。
5. 镜像 tag、`docker-compose.yaml`、`frontend/src/App.vue` 的 `APP_VERSION` 三者必须同步改，否则页脚版本号对不上镜像。
6. 推送成功会打印 `vX.Y.Z: digest: sha256:…`；没看到 digest 就是没成功（常见于未登录或网络中断）。
7. GitHub Actions 方式（推 `v*` tag 自动构建多架构）见 `README.md` §3 方式三 + `.github/workflows/docker-publish.yml`，
   需要仓库 Secrets 配 `ALIYUN_USERNAME` / `ALIYUN_PASSWORD`；本机目前推不了 GitHub（TLS 问题），这条路要在能通网的机器上触发。
