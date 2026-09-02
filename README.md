# MQTT 用户管理系统

基于 **Go + Vue 3** 的 Mosquitto MQTT broker 用户管理工具，通过 Web 界面完成用户注册、管理和 ACL 规则维护，并以 Docker 容器化部署。

> **来源声明**：本项目基于一个**长期未维护的 TC1 智能排插 MQTT 管理项目**二次开发而来。原项目未附带开源许可证（ALL RIGHTS RESERVED），本仓库为维护者在此基础上做**自维护构建与增强**，仅用于个人/小范围部署。若你 fork 或再分发，请保留本声明并注明来源。

## 功能特性

本版本在原有功能基础上做了如下**增强与修复**：
- 修复"设备同步（devices-sync）会清空已有设备"的问题（改为合并同步）
- MQTT 连接地址自动跟随浏览器访问地址（不再写死服务器地址）
- 配置修改后**持久化**到 `config.yaml`，重启不丢失
- ACL 规则追加时**合并到已有用户块**并去重（不产生重复块）
- 修复**定时任务配置丢失**问题（前端多层配置改为深合并）
- 新增**设备显示名称持久化**（`device_metadata.json`，独立于 ACL）
- 新增 **v1.4.0 设备配置持久化**（`device_config.json`）：插口名/备注、插口布局（auto/1×6/2×3/3×2）保存到后端，跨内网/外网/多浏览器一致；定时任务配置同步回显（仍由设备固件执行）
- 新增 **v1.4.2 配置合并修复**：后端对定时任务按“插口/任务键”合并保存、并对各字段（布局/备注/定时任务）增量更新，修复“刷新后定时任务丢失/只剩最后一个”、“插口备注覆盖定时任务”等问题
- 新增 **v1.4.3 前端版本号显示**：页面底部显示当前前端版本号，便于确认是否为最新部署
- 管理员登录期间禁止导入/覆盖配置

- **注册页** (`/register`)：用户自助注册，填写用户名、密码和设备 MAC 地址，后端自动创建 mosquitto 账户并生成 ACL 规则
- **管理页** (`/admin`)：管理员登录后可增删改用户、手动编辑 ACL 文件、开关注册功能
- **控制台** (`/`)：原有 MQTT 设备控制台（zTC1 智能插排）
- 同一端口通过 nginx 反代 API (`/api`) 和 MQTT WebSocket (`/mqtt`)

## 目录结构

```
mqtt_mgr/
├── backend/            # Go 后端
│   ├── main.go
│   ├── auth.go         # 管理员 Token 认证
│   ├── handler_register.go
│   ├── handler_admin.go
│   ├── store.go        # passwd/ACL 文件读写
│   ├── config.yaml     # 配置文件
│   └── Dockerfile
├── frontend/           # Vue 3 + Vite 前端
│   ├── src/
│   │   ├── views/
│   │   │   ├── RegisterPage.vue
│   │   │   └── AdminPage.vue
│   │   ├── components/ # 原有控制台组件
│   │   └── router.js
│   └── Dockerfile
├── nginx/
│   └── nginx.conf      # 反代配置
├── mosquitto/
│   └── config/         # mosquitto.conf（需手动创建）
├── docker-compose.yaml
└── build.sh            # 多架构镜像构建脚本
```

## 快速开始

> **最快方式**：repo 已自带 `mosquitto/config/mosquitto.conf` 和干净的 `backend/config.yaml` 模板。
> 只需改好 `backend/config.yaml` 里的 **admin 密码**，然后：
> ```bash
> bash init.sh
> ```
> 该脚本会自动创建目录、生成空的 passwd/acl 文件、拉取镜像并启动全部服务。

### 1. 配置 mosquitto

`mosquitto/config/mosquitto.conf` 示例：

```conf
listener 1883
listener 9001
protocol websockets
allow_anonymous false
password_file /mosquitto/config/passwd
acl_file /mosquitto/config/acl
```

初始化 passwd/acl 文件（**务必写入内容使其非空**，空文件会被 Docker 当成目录导致挂载失败）：

```bash
mkdir -p mosquitto/config mosquitto/data mosquitto/log
echo "" > mosquitto/config/passwd
echo "" > mosquitto/config/acl
```

### 2. 修改后端配置

编辑 `backend/config.yaml`：

```yaml
container_name: "mqtt-broker"        # docker container 名称
container_passwd_path: "/mosquitto/config/passwd"
host_passwd_path: "/data/passwd"     # 容器内挂载路径
acl_path: "/data/acl"

acl_patterns:                        # %s 替换为 MAC 地址（大小写各一份）
  - "device/ztc1/%s/#"
  - "homeassistant/+/%s/#"

admin_username: "admin"
admin_password: "your_password"      # 修改此项！
allow_register: true

listen_addr: ":8080"
```

### 3. 部署

**方式一：从阿里云拉取镜像直接部署**

```bash
docker compose pull
docker compose up -d
```

**方式二：本地构建**

```bash
# 构建前端
cd frontend && npm ci && npm run build && cd ..

# 构建并启动
docker compose up -d --build
```

### 4. 多架构镜像构建推送

```bash
# 需先 docker login registry.cn-hangzhou.aliyuncs.com
./build.sh          # tag=latest
./build.sh v1.0.0   # 自定义 tag
```

构建目标：`linux/amd64` + `linux/arm64`，推送至：
- `crpi-wjscuxkxm163odo3.cn-hangzhou.personal.cr.aliyuncs.com/dkl091219/mqtt-mgr-backend`
- `crpi-wjscuxkxm163odo3.cn-hangzhou.personal.cr.aliyuncs.com/dkl091219/mqtt-mgr-nginx`

> **架构说明**：`docker-compose.yaml` 默认拉取的是 **linux/amd64** 镜像（已在 x86_64 服务器验证）。
> 若目标机器是 **ARM**（树莓派、香橙派、玩客云等），需要先用 `./build.sh <版本号>` 构建并推送
> `linux/arm64` 镜像，然后自行修改 `docker-compose.yaml` 中的镜像 tag 后再部署。

## API 接口

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/ping` | 健康检查，WAF 拦截探测用 |
| GET | `/api/register/status` | 是否开放注册 |
| POST | `/api/register` | 用户注册 |
| POST | `/api/admin/login` | 管理员登录，返回 Token |
| GET | `/api/admin/users` | 获取用户列表（需 Token） |
| POST | `/api/admin/users` | 创建用户（需 Token） |
| DELETE | `/api/admin/user/:name` | 删除用户（需 Token） |
| PUT | `/api/admin/user/:name` | 修改密码（需 Token） |
| GET/PUT | `/api/admin/acl` | 读取/保存 ACL 文件（需 Token） |
| GET/PUT | `/api/admin/config` | 查看/修改运行时配置（需 Token） |
| GET | `/api/admin/device-config` | 获取本账号全部设备的配置（插口名/布局/定时任务）（需 Token） |
| PUT | `/api/admin/device-config/:mac` | 保存指定设备的配置（需 Token，MAC 须属于本账号） |

## 本地开发

```bash
# 启动后端
cd backend
go run .

# 启动前端（/api 代理到 :8080）
cd frontend
npm run dev
```

## WAF 配置说明

若在 nginx 前部署了 WAF（如雷池 SafeLine、Cloudflare 等）并对路径启用了人机认证，需注意以下事项：

### 问题背景

Vue 使用 History 路由模式，前端页面切换（如直接进入 `/register`）不会发出新的 HTTP 请求，WAF 无法感知到路径变化，因此**不会触发人机认证**。

### 解决方案

本项目在应用启动时会自动调用 `GET /api/ping`。当 WAF 拦截该请求（返回非 200，或连接被阻断）时，前端会自动执行 `window.location.href = '/register'`，触发一次真实的浏览器跳转，从而让 WAF 完成人机验证。

### WAF 规则配置建议

| 防护路径 | 说明 |
|----------|------|
| `/register` | 用户注册页（兜底触发验证页） |
| `/api` | 所有后端 API，含 `/api/ping` |

两个路径都需要纳入 WAF 防护，**缺一不可**：
- `/api` 被 WAF 拦截 → 前端探测到 → 跳转 `/register`
- `/register` 触发人机认证 → 通过后 WAF 放行 Cookie → 后续 `/api` 请求正常
