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
- **v1.9.0 全面代码审查修复版**（无新功能，修复以下问题）：
  - 安全：`skip_totp_on_intranet` 不再信任可伪造的 X-Forwarded-For 首值（改用 nginx 覆盖式 X-Real-IP）；用户名禁止 `:`/空白字符（防破坏 passwd/ACL 文件解析）
  - 功耗：修复小时聚合重复累加导致"本月用电"虚高，升级时自动从分钟表重建历史小时数据；采集 broker 地址改为可配置 `power_mqtt_broker`
  - 配置：管理员改 `admin_password` 后重启自动同步 mosquitto passwd（不再需要手动删用户）；定时任务支持真正删除（`tasks_replace` 全量替换语义 + 任务行删除按钮）
  - 前端：修复切换设备时定时任务/高级设置缓存串台；倒计时到期停用后不再被设备重连恢复"复活"；移动端访问不再覆盖服务端保存的插口布局；注册页一键添加的 MAC 大小写与主流程一致；管理员面板对 WAF/502 等非 JSON 响应容错

- **v2.1.0**（可靠性修复 + Webhook）：
  - ACL 文件读写加锁，避免并发注册/追加 MAC 写坏文件
  - `appendACLRules` 在用户块不存在时补写 `device/ztc1/set`（V2 控制必需）
  - V2 调度器改为 **发布成功后再记 fire**；失败不落库，本分钟后续 tick 可重试
  - 新增 **通用 Webhook**：检测到插口开关翻转（与任务日志同源，V1/V2/手动均可）时 HTTP POST 到外部机器人；管理页可配 URL/Header/Body 模板/重试，并支持「测试推送」
  - 模板变量：`${event}` `${mac}` `${mac_upper}` `${plug}` `${prev}` `${cur}` `${action}` `${time}` `${count}` `${message}` `${flips}`
  - **防刷屏**：`webhook_throttle_seconds`（默认 2s）窗口内多次翻转合并为一条 `plug_flip_batch`；0=不合并
  - 说明：Webhook **只出站**，不向排插下发 MQTT；需 `power_collect_enabled: true`（翻转检测挂在功耗采集订阅上）

- **v2.2.0**（功耗统计修正）：① 采集器补订 V2 的实时功率流 `homeassistant/sensor/<MAC>/power/state`
  （固件默认每 2 秒一条；此前只订 `device/ztc1/...`，而 V2 的 state 只在变化时发，导致采样极稀疏）；
  ② 能量积分上限从硬编码 30 秒改为 `max(30s, 3×近 10 次间隔中位数)`，分钟级上报的设备不再被系统性低估；
  ③ 小时聚合增加"补缺"（1min 有、1h 缺的小时都补算）并把周期从 1 小时缩到 10 分钟，
  "本月用电"不再因容器重启留下永久空洞。

- **v2.1.9**（"定时"徽标即时生效）：v2.1.8 只在打开"定时"面板时才查任务表，导致插口卡片上的数量徽标
  要进出面板一次才出现。现在**页面加载、切换设备、每 5 分钟**各主动查一遍 6 个插口（逐插口错峰 200ms，
  页面刚开 MQTT 未连上时按 0.8/3/8 秒补三次），徽标一进来就是准的。

- **v2.1.8**（网页能读到设备真实任务表）：v1 固件对 `setting` 的 state 回显是**增量**的（只回本次请求的键），
  所以手机 App 建的任务网页拿不到、看到的任务表常是残缺的。现在打开某插口的"定时"面板会先发一次
  **全槽位只读查询**（`{"mac":..,"plug_N":{"setting":{"task_0":{}…"task_4":{}}}}`），拿到该插口完整任务表。
  空对象不改设备；不放进 60 秒轮询是因为 6×5 的回显接近固件 MQTT 缓冲上限，有截断风险。

- **v2.1.7**（执行位置细化到每个任务）：任务对象新增 `exec` 字段——`"server"` 由后端按服务器时间执行
  （设备侧同名槽位写 `on:0` 防双触发），缺省/`"device"` 仍由排插自己执行、**后端完全不碰**
  （安卓 App 建的任务因此不受影响）。前端任务编辑器新增"执行位置：设备 / 服务器"（仅 v1 显示，v2 固定服务器），
  服务器任务在列表里带蓝色"服务器"标记。实测四种冲突场景全通过，详见 CHANGELOG。

- **v2.1.6**（V1 固件改由后端调度）：v1（原厂闭源固件）的定时任务原本靠排插自己的时钟，而那个时钟
  无法从外部校准（`setting.time` 在原厂固件里没有解析分支）。打开 `v1_backend_scheduling` 后，
  v1 任务改由后端按**服务器时间**执行（15s tick，去重/补发沿用 `scheduler_fires` + `scheduler_catchup_minutes`），
  并自动把**设备侧同名任务停用**（`on:0`，每分钟复查）防止双触发。默认关闭，不配时行为与 v2.1.5 一致。
  实测（生产 `a1b2c3d4e5f6` plug_0）：01:38:47 接管 → 01:41:10 触发 → 01:41:22 灯亮，一次到位。

- **v2.1.5**（V1 固件周期校时）：V1 排插不跑 NTP，时间只能由控制台推 `setting.time`，而此前**只有前端**
  在"打开页面/设备重连/手点按钮/存倒计时"时推，页面长期不开设备时钟就自由漂移（±20~50 ppm ≈ 1~2 分钟/月）。
  现在后端每 `v1_time_sync_interval_hours`（默认 6，`0`=关闭，上限 168）向所有 `firmware != v2` 的设备推一次时间，
  **启动即校一次**；只写 `setting.time`，不动插口开关，V2 设备跳过（它们的任务由后端按服务器时间执行）。
  排查：日志 `[timesync] ...` 与 `GET /api/admin/diagnostics/time-sync`（含 `missing_acl` —— mosquitto 丢包是静默的）。
  真机实测：V1 固件执行 MQTT 写入的定时任务时，实际动作**比设定分钟晚约 60 秒**（固件分钟级检查粒度，
  与 NTP/晶振无关）；校时本身无法回读设备时钟，只能靠行为验证。

- **v2.1.4**（前端构建 Node 升级）：`frontend/Dockerfile` 的 builder 从 `node:20-alpine` 钉到 **`node:24.22.1-alpine`**
    （当前最新 LTS 线）。JS 依赖未动，只换打包用的 Node；已在本地用 node24+npm11 复刻 CI 的 `npm ci && npm run build` 验通。
- **v2.1.3**（依赖升级）：`govulncheck` 可达漏洞 **3 → 0**（gorilla/websocket 1.5.3、paho.mqtt.golang 1.5.1、
    golang.org/x/net 0.61.0），并升到 `x/crypto 0.58.0` / `modernc.org/sqlite 1.61.0` 等当前稳定线。**Go 源码零改动**。
    跨版本最大的 sqlite 驱动单独验过：用你生产库的副本跑真程序，`integrity_check=ok`、读写正常、无迁移异常。

- **v2.1.2**（Go 工具链安全升级）：构建镜像从 `golang:1.23-alpine` 钉到 **`golang:1.27.3-alpine`**，
    `go.mod` 语言版本对齐到 `go 1.27`。Go 的标准库漏洞是**编译期**带进二进制的，所以必须重编重发才有效
    （2026-10-08 的 `go1.26.9/go1.27.2` 修 `crypto/tls`/`net/http`/`net/textproto`/`os`，
    2026-10-19 的 `go1.26.10/go1.27.3` 修 `crypto/x509`）。
    新工具链的 `go vet` 还抓到并修复了 `email.go` 里拼 SMTP 地址不用 `net.JoinHostPort` 的老 bug（IPv6 会拨号失败）。

- **v2.1.1**（会话失效可见 + 定时补发）：
  - 控制台页 `/` 不再静默吞掉管理员会话过期：`GET/PUT /api/admin/device-config` 遇 401 立即弹窗并退回登录页
    （此前只 `console.warn`，界面看着"已保存"，刷新就没了 —— 实测造成定时任务连续几天没写进服务器）
  - 写回失败（403/500 等）同样可见，提示"界面显示只是浏览器本地状态，刷新会丢"
  - V2 调度器新增**错过补发窗口** `scheduler_catchup_minutes`（未配置=30，`0`=关闭并退回"只认当前分钟"，上限 120）：
    容器重启 / NAS 休眠导致错过那一分钟时，窗口内补发，日志标 `已补发`；生效值打印在启动日志
  - 新增 `backend/scheduler_test.go`，其中一条断言锁住回滚把手：窗口=1 分钟时与 v2.1.0 逐分钟判定等价
  - **移除上游自带的第三方访问统计**：`frontend/index.html` 里的 `umami.fjrcn.cn` 脚本（v1.4.5 基线带入）已删，
    控制台不再向外部上报页面访问，也不再注入一段无完整性校验的外部 JS；功耗统计等全部功能走本地接口，不受影响

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

**方式三：GitHub Actions（推送 git tag 自动构建）**

公开仓库在推送 `v*` 标签时，会自动构建 `linux/amd64` + `linux/arm64` 并推到阿里云：

```bash
git tag v2.1.1
git push origin v2.1.1
```

需在仓库 Secrets 配置 `ALIYUN_USERNAME` / `ALIYUN_PASSWORD`。workflow 见 `.github/workflows/docker-publish.yml`。

### 4. 多架构镜像构建推送

```bash
# 需先登录阿里云 ACR（用户名=阿里云账号全名，密码在 ACR「访问凭证」页设置）
docker login --username=dkl091219 crpi-wjscuxkxm163odo3.cn-hangzhou.personal.cr.aliyuncs.com
./build.sh          # tag=latest
./build.sh v1.0.0   # 自定义 tag
```

构建目标：`linux/amd64` + `linux/arm64`，推送至：
- `crpi-wjscuxkxm163odo3.cn-hangzhou.personal.cr.aliyuncs.com/dkl091219/mqtt-mgr-backend`
- `crpi-wjscuxkxm163odo3.cn-hangzhou.personal.cr.aliyuncs.com/dkl091219/mqtt-mgr-nginx`

> ACR 的登录/推送细节、NAS 上的构建流程、上线切换与回滚，全部整理在 **[DEPLOY.md](DEPLOY.md)**（含 VPC 内网域名与已知坑）。

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
| PUT | `/api/admin/device-config/:mac` | 保存指定设备的配置（需 Token，MAC 须属于本账号；`tasks_replace:true` 时任务全量替换） |

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
