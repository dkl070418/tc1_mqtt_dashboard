# 部署指南（公开仓库占位版）

> 本文件为脱敏版本。真实内网 IP、SSH 用户与密钥路径请参考本地 handoff，勿写入公开仓库。

## 阿里云镜像仓库

- 地址: `crpi-wjscuxkxm163odo3.cn-hangzhou.personal.cr.aliyuncs.com/dkl091219`
- 镜像: `mqtt-mgr-backend`, `mqtt-mgr-nginx`
- 可匿名拉取；推送需先在阿里云控制台创建对应镜像仓库并 `docker login`

## 构建推送（在你的 Linux/Docker 构建机上）

```bash
# 后端
docker build --build-arg GOPROXY=https://goproxy.cn,direct \
  -t <registry>/mqtt-mgr-backend:<tag> ./backend

# 前端/nginx（构建 context 为项目根）
docker build --build-arg NPM_REGISTRY=https://registry.npmmirror.com \
  -t <registry>/mqtt-mgr-nginx:<tag> -f frontend/Dockerfile .

docker push <registry>/mqtt-mgr-backend:<tag>
docker push <registry>/mqtt-mgr-nginx:<tag>
```

## 部署

参见仓库根目录 `docker-compose.yaml` 与 `init.sh`。改好 `backend/config.yaml` 中的 admin 密码后再启动。

## 版本号同步

- `frontend/src/App.vue` 的 `APP_VERSION`
- `docker-compose.yaml` 中的镜像 tag
- 本地 `CHANGELOG.md` / handoff
