# 部署指南

## NAS 信息

- IP: `192.168.15.201`
- SSH 用户: `root`（密钥: `C:\Users\DengKailin\.ssh\id_ed25519_tc1`）
- Docker 登录用户: `dkl091219`（需在 NAS 上手动 `docker login`）
- 项目路径: `/tmp/build_180/`

## 阿里云镜像仓库

- 地址: `crpi-wjscuxkxm163odo3.cn-hangzhou.personal.cr.aliyuncs.com/dkl091219`
- 镜像: `mqtt-mgr-backend`, `mqtt-mgr-nginx`

## 构建推送流程

### 1. 同步代码到 NAS

```powershell
scp -i C:\Users\DengKailin\.ssh\id_ed25519_tc1 -o StrictHostKeyChecking=no "D:\TOOLS\tc1_mqtt-master\frontend\src\App.vue" root@192.168.15.201:/tmp/build_180/frontend/src/App.vue
```

### 2. NAS 上执行构建（用 dengkailin 用户）

NAS 的 Docker Hub 被墙，buildx 的 buildkit 容器拉不到镜像。解决方案：**用本地 docker daemon 直接构建**，不走 buildx。

```bash
# SSH 到 NAS
ssh -i C:\Users\DengKailin\.ssh\id_ed25519_tc1 root@192.168.15.201

# 用 dengkailin 用户执行（有 docker login 凭据）
su - dengkailin

# 先通过国内镜像源拉取基础镜像并 tag 回原名
docker pull docker.1panel.live/library/golang:1.23-alpine && docker tag docker.1panel.live/library/golang:1.23-alpine golang:1.23-alpine
docker pull docker.1panel.live/library/alpine:latest && docker tag docker.1panel.live/library/alpine:latest alpine:latest
docker pull docker.1panel.live/library/node:20-alpine && docker tag docker.1panel.live/library/node:20-alpine node:20-alpine
docker pull docker.1panel.live/library/nginx:alpine && docker tag docker.1panel.live/library/nginx:alpine nginx:alpine

# 构建 backend
cd /tmp/build_180
docker build -t crpi-wjscuxkxm163odo3.cn-hangzhou.personal.cr.aliyuncs.com/dkl091219/mqtt-mgr-backend:TAG ./backend

# 构建 nginx（前端）
docker build -t crpi-wjscuxkxm163odo3.cn-hangzhou.personal.cr.aliyuncs.com/dkl091219/mqtt-mgr-nginx:TAG -f frontend/Dockerfile .

# 推送
docker push crpi-wjscuxkxm163odo3.cn-hangzhou.personal.cr.aliyuncs.com/dkl091219/mqtt-mgr-backend:TAG
docker push crpi-wjscuxkxm163odo3.cn-hangzhou.personal.cr.aliyuncs.com/dkl091219/mqtt-mgr-nginx:TAG
```

### 3. NAS 上更新服务

```bash
cd /tmp/build_180
# 更新 docker-compose.yaml 里的镜像 tag
sed -i 's/mqtt-mgr-backend:.*/mqtt-mgr-backend:TAG/' docker-compose.yaml
sed -i 's/mqtt-mgr-nginx:.*/mqtt-mgr-nginx:TAG/' docker-compose.yaml
docker-compose pull backend nginx
docker-compose up -d backend nginx
```

### 4. 本地前端构建验证（可选）

```bash
cd D:\TOOLS\tc1_mqtt-master\frontend
npm run build
# 验证版本号
Select-String -Path dist\assets\App-*.js -Pattern "TAG"
```

## 注意事项

- `/tmp/build_180/` 下的 Dockerfile 被临时改了 FROM 地址加了 `docker.1panel.live` 镜像源，这是为了绕过 Docker Hub 被墙。源码仓库里的 Dockerfile 保持原样不变。
- `build.sh` 用的是 buildx 多架构构建，但在 NAS 上因为墙会失败。上面的手动流程用本地 daemon 单架构构建，NAS 是 amd64 所以够用。
- 后端代码也有改动（auth.go 内网跳过 TOTP、handler_admin.go、main.go、AdminPage.vue），如果这些改动也需要部署，需要同步整个 backend 目录到 NAS。
