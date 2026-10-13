# Changelog

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
