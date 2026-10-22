# Changelog

## v2.1.3（依赖升级，本地未部署）

`govulncheck` 实测：**升级前代码可达 3 个漏洞 → 升级后 0 个**（`go run golang.org/x/vuln/cmd/govulncheck@latest ./...`，工具链 go1.27.3）。
**Go 源码一行没改**（paho v1.5 保留了我们用到的全部 API），只有 `go.mod` / `go.sum`。

### 修掉的三个（都是"我们的代码确实会调用到"）

| 编号 | 模块 | 旧 → 新 | 内容 |
|------|------|---------|------|
| GO-2026-6278 | gorilla/websocket | v1.5.0 → **v1.5.3** | WebSocket 掩码密钥用了弱 PRNG；经 paho 的 websocket 连接路径可达 |
| GO-2025-4173 | eclipse/paho.mqtt.golang | v1.4.3 → **v1.5.1** | 字符串超过 65535 字节时编码错误；调度器/采集器的 Connect·Publish 可达 |
| GO-2025-3503 | golang.org/x/net | v0.10.0 → **v0.61.0** | HTTP 代理可用带 Zone ID 的 IPv6 绕过；经 paho 的 `proxy.FromEnvironment` 可达 |

其余依赖一并升到当前稳定线：`x/crypto v0.17.0→v0.58.0`、`x/sync v0.1.0→v0.24.0`、`x/sys v0.22.0→v0.49.0`、`modernc.org/sqlite v1.34.5→v1.61.0`（含 libc/mathutil/memory 与 humanize·strftime·isatty 间接项）。

残留 1 条属**不可操作**：GO-2026-5932 = `golang.org/x/crypto/openpgp` 包"不再维护、设计上不安全"，`Fixed in: N/A`；我们没 import 该包（扫描同时报 `0 vulnerabilities in packages you import`）。

### 为什么这次升级要实测两条链路（sqlite 跨了 26 个小版本）

| 判据 | 读数（NAS 上一次性 broker + 独立临时目录，跑完已清） |
|------|------|
| 调度器经 **paho v1.5.1** 真实发消息 | broker 侧 `mosquitto_sub` 抓到 `device/ztc1/set set socket D0BAE4637BE5 1 1`（不看我们的日志，看线上真发了什么） |
| 采集器经 v1.5.1 收 + **sqlite v1.61** 写 | 灌两条设备状态 → `power_samples=2`、`task_logs=1`（翻转 1→0）、`scheduler_fires=1`（30s 内多个 tick 只触发一次） |
| **打开你现有的生产库文件**（由旧驱动写的 22 MB 库，含 -wal/-shm 一起复制） | 前后 `pragma integrity_check=ok`；新程序写入后 `power_samples 210488→210489`、`agg_1min 73482→73483`、`scheduler_fires 10→11`，无报错、无迁移异常；生产原件只被复制未被写 |
| `go build` / `go vet` / `go test`（go1.27.3） | 全部通过 |
| 首轮"power_samples=0" | 是**我取证脚本自己等太短**：采样是异步批写、ticker 10 秒 flush；把等待改成 16 秒后即为上表读数。同时暴露一个既有小行为：进程被 SIGKILL 时最多丢 10 秒采样（本次没改它） |

### 没动的东西

- mosquitto 镜像仍钉 `eclipse-mosquitto:2.0.18`；`frontend` 的 npm 依赖本次未动（要升单独说）。
- v2.1.1/v2.1.2 的行为改动没有任何回退。

### 回滚

`backend/go.mod` + `backend/go.sum` 回退到上一版即可（无源码耦合）；或直接 compose tag 回 `v2.1.2`。

## v2.1.2（Go 工具链安全升级，本地未部署）

### 为什么发这一版

Go 官方 2026-10-08 / 2026-10-19 两次安全补丁：`go1.26.9`/`go1.27.2` 修 `crypto/tls`、`net/http`、`net/textproto`、`html/template`、`os`；`go1.26.10`/`go1.27.3` 修 `crypto/x509`（go.dev Release History 读数）。
**Go 服务的漏洞是编译期带进二进制的**（标准库静态链接），所以只要镜像是用旧工具链构建的，换 base image 不解决问题——必须重新编译发布。本项目此前用 `golang:1.23-alpine`，v2.1.0/v2.1.1 的镜像都落在受影响区间内。

### 改动

| 位置 | 改动 |
|------|------|
| `backend/Dockerfile` | builder `golang:1.23-alpine` → **`golang:1.27.3-alpine`**（钉死补丁号以便审计；想自动跟随后续补丁就改成 `golang:1.27-alpine`，一行） |
| `backend/go.mod` | `go 1.23.3` → `go 1.27`，与构建工具链同线（Go 的部分兼容默认值按 `go` 行取值，钉在旧语言版本可能保留旧行为） |
| `backend/email.go` | 拼 SMTP 地址 `fmt.Sprintf("%s:%d")` → `net.JoinHostPort`：新工具链的 `go vet` 报出 `address format "%s:%d" does not work with IPv6`，这是**我们代码里原有的真 bug**（IPv6 字面量拨号会失败），不是 Go 的回归 |
| `frontend/src/App.vue` / `docker-compose.yaml` | 版本号 / 镜像 tag → v2.1.2 |

### 本项目的实际暴露面（读了代码，不夸大）

- 后端只监听明文 HTTP `:8080`，TLS 由外层 nginx/WAF 负责 → `crypto/tls` 的服务端路径不在这条上。
- 真用了出站 TLS 的地方：SMTP（`smtp.qiye.aliyun.com:465`，`tls.DialWithDialer`）和 Webhook（HTTPS POST）→ `crypto/tls` + `crypto/x509` 的客户端路径，以及 `net/textproto`（SMTP 客户端就是 net/textproto 之上）。这批包**确实被链接进我们的二进制**。
- `html/template` 前端是 Vue，后端没用模板 → 不在面内。
- ⚠️ 未一手核实：具体 CVE 编号我没能从 go.dev 公告里逐条取到（release history 只列了包名）。第三方分析把 `crypto/tls` 那条写成 CVE-2026-56862 / GO-2026-6090（DoS，CVSS 7.5），我未复核，仅作背景，不作为结论。

### 验证

| 判据 | 读数 |
|------|------|
| `go version`（本机） | go1.27.3 windows/amd64 |
| `go vet ./...` / `go build ./...` / `go test ./...` | 全部通过（vet 在修掉 email.go 那条后重新变干净） |
| `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build` 后 `go version <产物>` | `go1.27.3` ✔ 证明产物内嵌的正是修复版工具链 |
| CI 出的 v2.1.2 镜像 | 发布后在 NAS 上 pull 下来 `go version` 复算（CI success ≠ 改动进了镜像，这条必须实测） |

### 回滚

- 工具链回滚：`backend/Dockerfile` 的 FROM 改回 `golang:1.23-alpine`（一行）；`go.mod` 的 `go 1.27` 改回 `go 1.23.3`。
- 版本回滚：compose 两个 tag 回 `v2.1.1`（阿里云上镜像仍在）。

## v2.1.1（本地未部署）

针对 2026-10-13 在 NAS 上取证的"定时任务设好后像没存一样消失"。

### 根因（两条互相放大）

1. 管理员令牌默认 **7 天**过期。生产读数：最后成功登录 `2026-10-01 09:35 UTC`，最后一个 200 是 `10-08 06:12`，
   从 `10-08 10:54` 起 `/api/admin/*` **全部 401**（含 `GET/PUT /api/admin/device-config`）。
2. 控制台页 `/` 把失败全吞了：`loadDeviceConfigs()` 遇非 2xx 直接 `return`，`persistTasks()/persistRemarks()` 只
   `console.warn`。界面照样显示"已保存"，刷新拿不到配置 → 看起来就是"执行一次就不见了"。
   管理页 `/admin` 本来就有 401 自动登出，控制台没有——这条补齐。
3. 连带后果：401 后 `deviceConfigs` 为空 → `getDeviceVersion()` 回退 `'v1'` → 控制台会把 V2 设备按 V1 直接把任务
   用 MQTT 推给固件（MQTT 凭据不过期，仍可用），所以"确实动过一次"。

### P0 前端：会话失效 / 写回失败必须可见

| 位置 | 改动 |
|------|------|
| `App.vue` | 新增 `notifyAdminError()`（60s 节流，防倒计时清理循环刷屏）与 `handleAdminUnauthorized()`（弹窗 + 复用 `logoutAdmin()`） |
| `App.vue` | `saveDeviceConfig()`：401 → 提示并退出管理员态；其它非 2xx → 提示"保存到服务器失败：…（界面显示只是本地状态，刷新会丢）"后抛出 |
| `App.vue` | `loadDeviceConfigs()`：401 → 打开页面即告知，不再静默降级成 v1 |
| `App.vue` | `saveDeviceDisplayName()` / `fetchTaskLogs()`：401 同样触发；重新登录后复位提示位 |
| `App.vue` | `APP_VERSION = v2.1.1` |

### P1 后端：V2 调度器错过补发

| 位置 | 改动 |
|------|------|
| `backend/scheduler.go` | `tick()` 改用 `dueOccurrence()`：回看今天/昨天最近一次"应执行时刻"，落在窗口内且该分钟未触发过即下发（覆盖容器重启 / NAS 休眠 / 那一分钟发不出去） |
| `backend/main.go` | 新增 `scheduler_catchup_minutes`（`*int`：未配置=30，0=关闭，上限 120） |
| `backend/config.yaml` | 显式写入 `scheduler_catchup_minutes: 30` 并注释；启动日志打印生效值（不留不可见的编译期默认值） |
| `backend/scheduler_test.go` | 新增：按时 / 窗口内补发 / 超窗不补 / 星期掩码 / 跨零点回看 / 一次性不回看昨天；**窗口=1 分钟时与旧规则逐分钟等价**（4 天 × 4 掩码 × 1440 分钟 × 2 秒点）；`fire_minute` 与旧版同串 |

**一行回滚**：`scheduler_catchup_minutes: 0` —— 发布序列与 v2.1.0 逐条相同（同 topic、同 payload、同 fire_minute）。

### 隐私：去掉上游自带的第三方访问统计

| 位置 | 改动 |
|------|------|
| `frontend/index.html` | 删除 `<script src="https://umami.fjrcn.cn/script.js">`（v1.4.5 基线导入时就带的 Umami 站点统计，`data-website-id=8ca010ef-…`；此后本仓只有那一笔提交动过 `index.html`） |

- 删前删后各跑一次无头浏览器：网络轨迹里 `200 https://umami.fjrcn.cn/api/send` ×2 → **0 条**；页面照常启动（6 个插口卡片渲染），A/B 两个场景仍 PASS。
- 零功能损失：功耗图走本地 `/api/admin/power/*` + SQLite。附带收益是管理页不再被一段无 SRI 校验的外部 JS 注入，也不再需要外网。

### 自证（不需要你上机）

| 判据 | 读数 |
|------|------|
| `go vet ./...` / `go build ./...` / `go test` | 全部通过 |
| NAS 一次性 broker 实跑（`/tmp/tc1_proof`，与生产隔离，跑完已清） | `catchup=30`：6 分钟前的任务出现 `已补发 … 迟到 6 分钟 -> set socket D0BAE4637BE5 1 1`，`scheduler_fires` 恰 1 行（去重生效）；`catchup=0`：0 次下发、0 行 |
| 真实 Go 后端的 401 契约 | `GET` 与 `PUT /api/admin/device-config/…` → `401 {"error":"令牌无效或已过期"}` |
| 无头浏览器（打改动后的 dist） | 场景 A 失效令牌：弹窗 + 会话键清空 + 跳 `/admin`；场景 B 403：弹窗"保存到服务器失败"且不退出有效会话 |

### 事故交代（我造成的，已核对无残留）

自证收尾时我用 `kill <pid>` 清理自己的临时进程，误杀了生产 `mqtt-mgr-backend`——容器内进程名同为 `mqtt_mgr`，
在 WSL 主机命名空间里 PID 可见且同名。`restart: unless-stopped` 立即拉起，实际不可用约 1 秒。事后核对：
`pragma integrity_check=ok`、功耗采样继续、`device_config.json` 与 `config.yaml` 的 mtime 未变、broker/nginx 均未重启。
新规矩：**临时进程只允许 kill「本次记录的 PID 且 `/proc/PID/cwd` 属于本次工作目录」的那一个**，禁止按进程名 `pkill/pgrep`。

## v2.1.0（本地未部署）

### P1 可靠性

| 位置 | 改动 |
|------|------|
| `backend/store.go` | 新增 `aclMu`，`addACLRules` / `appendACLRules` / `removeUserACL` / `writeACL` 全部串行化 |
| `backend/store.go` | `appendACLRules` 无用户块分支补写 `topic readwrite device/ztc1/set` |
| `backend/scheduler.go` | V2 定时：Publish 成功才 `MarkSchedulerFire`；失败本分钟约 15s 后可重试，避免“记了 fire 却没发出去” |

### Webhook（插口翻转推送）

| 位置 | 改动 |
|------|------|
| `backend/webhook.go` | 模板渲染、Header 解析、异步 POST + 有限重试、**节流合并**、`/api/admin/webhook/test` |
| `backend/mqtt_collector.go` | `detectPlugFlips` 里对每次翻转调用 `notifyWebhookAsync`（与 task_logs 同源） |
| `backend/main.go` / `handler_admin.go` | 配置字段 `webhook_*` 读写 + 持久化 |
| `frontend/.../AdminPage.vue` | 配置页 Webhook 表单（URL/Header/Body/重试/节流/测试） |
| `frontend/.../App.vue` | `APP_VERSION = v2.1.0` |

**防刷屏节流** `webhook_throttle_seconds`（默认 2，界面可选 0/1/2/5）：
- 窗口内多次翻转合并为一条，`event=plug_flip_batch`，`${count}` `${flips}` `${message}` 描述全部变化
- 单次翻转仍为 `plug_flip`；模板保留首条事件的 `${mac}/${plug}/${action}` 兼容
- `0` = 关闭节流；最多缓存 40 条；测试按钮不走节流

**v2.1.0 补丁**：
1. 功耗/翻转采集改为 **双大小写订阅**（V2 用大写 MAC 发布，原先收不到翻转）。
2. Webhook 文案带 **设备显示名 + 插口名**：`device_config` 备注优先，其次 MQTT `name`/`setting.name`；新变量 `${device}` `${plug_name}`。

### 前端顶栏 & 2FA（v2.1.0 同日热更）

- 标题「zTC1 管理终端」左对齐；右侧：主题 / 刷新 / 📦配置迁移 / 未登录时「注册 + ⚙️管理员入口」；已登录显示用户名 + 登出
- 管理员登录：表单不再常驻 2FA 输入框；密码正确且服务器要求验证码时，**模糊遮罩弹窗**输入 6 位数字（支持自动跳格、Backspace 回退）
- 说明：生产 `skip_totp_on_intranet=true` 时内网登录会直接放行、不弹窗——这是设计如此，不是“记住天数绕过 2FA”

### 配置示例（config.yaml）

```yaml
webhook_enabled: true
webhook_url: "https://example.com/notify"
webhook_headers: |
  Authorization: Bearer xxx
webhook_body: '{"event":"${event}","mac":"${mac}","plug":${plug},"action":"${action}","time":"${time}","count":${count},"message":"${message}"}'
webhook_retry: 1
webhook_throttle_seconds: 2
```

### 对排插的影响（结论：无额外 MQTT 负载）

- Webhook：后端 → 机器人，**不经过排插**。
- 调度器：成功路径仍是每任务每分钟 **一条** 短文本 `set socket ...`；仅失败时本分钟内重试。
- 未改动前端「刷新就批量下发任务」逻辑；v1.9.8 的错峰下发保持不变。

### 部署提醒

1. 本地 `go vet` / `go build -o NUL` 已通过（Windows exe 会被火绒隔离，构建在 NAS Docker）。
2. **已部署生产**：2026-09-14，backend+nginx 均为 `v2.1.0`（SSH 钥匙为 `~/.ssh/id_ed25519`，非 handoff 旧名 `id_ed25519_tc1`）。
3. 备份：`/mnt/smb_a/Ubuntu-22.04/docker/tc1/backup_v210/`
4. 浏览器需 **Ctrl+F5** 强刷前端缓存。
5. 本地 `docker-compose.yaml` tag 已同步 v2.1.0。
