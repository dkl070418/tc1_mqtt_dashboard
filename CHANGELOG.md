# Changelog

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
