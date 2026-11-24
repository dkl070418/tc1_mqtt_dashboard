# Changelog

## v2.1.9（"定时"徽标不用再点开面板才出现）

### 问题

v2.1.8 只在**打开某个插口的"定时"面板时**才查任务表。于是插口卡片上"定时 ②"那个数量徽标
要等你点进面板再退出来才出现——因为在此之前页面根本没有该插口的任务数据。

### 改动

| 位置 | 改动 |
|------|------|
| `frontend/src/components/DeviceControl.vue` | 新增 `queryAllPlugTasks()` / `schedulePlugTaskQueries()`：**页面加载、切换设备、每 5 分钟**各主动查一遍 6 个插口的任务表（逐插口错峰 200ms）；页面刚打开时 MQTT 可能还没连上，按 0.8s/3s/8s 补三次；组件卸载时清掉定时器与残留 setTimeout。v2 设备跳过 |

徽标本身不用改：`getEnabledTaskCount()` 读的是设备 state（已被查询填充）+ 服务器配置缓存。

### 验证（2026-11-24 10:17，生产栈 v2.1.9，设备 `a1b2c3d4e5f6`）

| 检查 | 结果 |
|---|---|
| 部署 | 两容器 `v2.1.9`，`/api/ping` 200；bundle 含 `v2.1.9` 与错峰循环 `mt<6;mt++ setTimeout(…,mt*200)` |
| 六个插口逐个查询 | 全部有回显：`plug_0` 启用 **2** 条（`task_1 00:00 rep=18 act=1`、`task_2 00:00 rep=48 act=0`），`plug_1~5` 均为 0 条 |
| 结论 | 页面加载即能算出插口1 的徽标 = 2，**不需要再点进"定时"面板** ✅ |
| 只读性 | 查询前后设备任务表一致，未产生写入 ✅ |

## v2.1.8（网页能读到设备上的真实任务表）

### 为什么

v1 固件对 `setting` 的 **state 回显是"增量"的**：只回显本次请求里带的键，不是整张任务表。
于是：

- 手机 App 建的任务，网页界面**根本拿不到**（实测：网页轮询发 `{setting:{mqtt_uri:null}}`，
  回显里连 `plug_0.setting` 都不含）；
- 就算拿到了，也常常是"最后一次写入的那个任务"，看起来像任务凭空消失。

### 改动

| 位置 | 改动 |
|------|------|
| `frontend/src/components/DeviceControl.vue` | 新增 `queryPlugTasks(plugIndex)`：打开某插口的"定时"面板时，发一次**全槽位只读查询** `{"mac":..,"plug_N":{"setting":{"task_0":{},"task_1":{},"task_2":{},"task_3":{},"task_4":{}}}}`。空对象不会改设备（固件只有在 hour/minute/repeat/action/on 齐全且为数字时才写入），只触发一次完整回显。v2 设备跳过（它的任务在服务器配置里） |
| 版本号 | `APP_VERSION` / compose tag → v2.1.8 |

**为什么不放进 60 秒轮询**：6 个插口 × 5 个任务的回显约 2KB，接近固件 MQTT 接收缓冲上限，
整包有被截断的风险；只在用户真正要看任务时问一次，既准又安全。

### 验证（2026-11-24 09:40，生产栈 v2.1.8，设备 `a1b2c3d4e5f6`，只动 plug_0/plug_2）

| 检查 | 结果 |
|---|---|
| 部署 | 两容器升到 v2.1.8，`/api/ping` 200；前端 bundle 含 `v2.1.8` 与新载荷 `task_0:{},task_1:{}…` |
| 模拟 App 写入 | 向 plug_2 写 `task_3 = 06:30 repeat=127 action=1 on=1`（与 App 同形态）→ 设备接受 |
| 新查询读回 | 同一插口用新载荷查询 → 回显 `task_3: 06:30 repeat=127 action=1 on=1`，启用任务数 1 ✅ |
| 读回 App 建的任务 | 查询 plug_0 → 回显 `task_1: 00:00 repeat=18 action=1 on=1`、`task_2: 00:00 repeat=48 action=0 on=1`（即手机 App 建的两条）✅ |
| 查询只读 | 连续两次查询，回显内容完全一致（空对象不会改任务）✅ |
| 清理 | 把测试用的 plug_2/task_3 写 `on=0`，复核启用任务数 0 ✅ |

> 注：真机验证替代不了"点开面板看 UI"，但已验证**协议链路与前端载荷一致**；
> 用户下次打开某个插口的"定时"面板即可看到设备上的真实任务表。

## v2.1.7（执行位置细化到"每个任务"）

### 为什么

v2.1.6 的 `v1_backend_scheduling` 是**全局**开关：一开就把 v1 所有启用任务都收归后端，
结果网页里建的任务会从设备上被停用 —— **安卓 App（zControl）看到的就是"任务被关掉了"**。
而 App 只能跟设备说话，它无从知道服务器上还有调度器。所以需要一个"这条任务归谁执行"的答案。

### 改动：任务粒度 + 前端可选

| 位置 | 改动 |
|------|------|
| 任务对象 | 新增 `exec` 字段：`"server"`（后端按服务器时间执行）或 `"device"`/缺省（排插自己执行） |
| `backend/scheduler.go` | `taskExec()` 解析执行位置；`loadScheduledTasks()` 对 v1 **只接管 `exec="server"` 的任务**，其余（含安卓 App 建的、以及缺省字段的老任务）**后端一律不碰**；v2 仍是全部后端执行。`v1_backend_scheduling` 降级为"总闸"（关闭时连 exec=server 也不接管） |
| `frontend TaskEditor` | 新任务表单加"执行位置：设备 / 服务器"（仅 v1 显示，v2 固定服务器）；任务行上服务器任务显示蓝色"服务器"标记；选服务器时给出一行提示 |
| `frontend DeviceControl` | `updateTask()` 三态：v2 → 只写服务器；v1+server → 只写服务器**并立刻把设备侧同名槽位写 `on:0`**（把双触发窗口从 1 分钟压到 0）；v1+device → 老行为（写设备） |
| `frontend App.vue` | MQTT 重连后的任务恢复**跳过 `exec="server"` 的任务**，不再把它们写回设备 |
| `frontend 倒计时` | 显式带 `exec`（v1=device、v2=server），保持原语义 |
| `backend/v1_sched_test.go` | 用例改为覆盖 exec 分流：exec=server 纳入、exec=device/缺省排除、总闸关闭全排除、`taskExec` 解析（含大小写不宽容） |

### 三种来源的实际后果（设计意图）

| 任务建在哪 | 谁执行 | 设备侧同名槽位 | 安卓 App 里看到 |
|---|---|---|---|
| 安卓 App（网页配置里没有） | 排插 | 原样保留 | 正常，可继续管理 |
| 网页控制台，选"设备" | 排插 | 正常写入 | 正常，可继续管理 |
| 网页控制台，选"服务器" | 后端 | 写 `on:0` 停用 | 显示为关闭（这是刻意的，防双触发） |

### 冲突场景实测（2026-11-24 03:23~03:27，生产栈 v2.1.7，设备 `a1b2c3d4e5f6`）

| 场景 | 做法 | 结果 |
|---|---|---|
| **A. 设备专属任务**（模拟安卓 App 建的，服务器配置里没有） | 直接向设备写 `plug_2/task_0 = 06:22 repeat=127 on=1` | 75 秒后设备上报仍 `on=1`；后端日志里"插口2"出现 **0** 次 → **后端完全无视，App 的任务不受影响** ✅ |
| **B. 服务器执行**（`exec="server"`） | 服务器配置 `plug_0/task_0 = 03:26 repeat=0 action=1 exec=server`，先把灯关掉 | `03:23:56` 日志"已接管 v1 任务…设备侧副本已停用"；`03:26:11` 触发（整分后 11 秒）；灯亮 ✅ |
| **C. 冲突**（设备侧同名槽位 on=1 + 服务器侧 exec=server） | 先向设备写 `plug_0/task_0 on=1`，再 PUT 同槽位 `exec=server` | 30 秒内设备侧被改成 `on=0`（`task_0:on=0 03:26 rep=0`）；最终只触发**一次**（日志仅 1 条 `已触发(v1)`）→ **无双重触发** ✅ |
| **D. 服务器侧 `exec="device"`** | 服务器配置 `plug_1/task_0 = 同一分钟 action=0 exec=device` | 该分钟过去后日志里"插口1"触发记录 **0** 条；设备侧也没有这条任务 → **后端确实不调度它** ✅ |

收尾：服务器配置恢复原样、设备六路全部 `on=1`、设备侧无启用任务、`/api/ping` 200。

### 已知限制

- **服务器执行的任务在安卓 App 里看不到明细**（设备侧被 `on=0`），App 里会显示为"该槽位任务被关闭"。
  这是防双触发的必然结果——一条任务不能同时被两个时钟调度。
- 设备上直接建的任务（App 建的）**不会自动出现在网页的服务器配置里**；网页界面能通过 MQTT 状态显示它们，
  但要纳入服务器调度，需要在界面里再保存一次并选"服务器"。

## v2.1.6（V1 固件改由后端调度）

### 为什么发这一版

v1（原厂闭源固件）的定时任务由**排插自己按它的时钟**执行，而那个时钟无法从外部校准：
`setting.time` 在原厂固件里**没有解析分支**（zTC1 v1.1.0 源码旁证 + 真机 8 组实验，详见
`docs/固件与定时任务链路.md`）。后果是任务"时好时坏、整体往后漂"，而 v2.1.5 的后端 6 小时校时对它无效。

### 改动

| 位置 | 改动 |
|------|------|
| `backend/main.go` | 新增 `v1_backend_scheduling`（bool，默认 `false`）；`/api/admin/config` GET 返回该开关与校时间隔 |
| `backend/scheduler.go` | `loadV2ScheduledTasks` → `loadScheduledTasks`：按固件分流。v2 走共享 topic + 文本命令；**v1 走每设备 topic + JSON `{"mac":..,"plug_N":{"on":0\|1}}`**，判定一律用**服务器时间**（15s tick），去重/补发沿用 `scheduler_fires` + `scheduler_catchup_minutes` |
| 同上 | 新增 `adoptV1Tasks`：开启后把**设备侧同名任务写 `on:0`**（每分钟复查一次，日志只在首次接管打印），避免固件按自己那个不准的时钟再触发一次（双触发） |
| `backend/config.yaml` | 写明开关、语义与回滚方式 |
| `backend/v1_sched_test.go` | 新增 3 组用例：v1 开关命令与前端同构、接管载荷必须"完整任务 + on:0"、按固件分流（v2 恒托管 / v1 受开关控制 / `on=0` 不进调度） |

**默认关闭**：不配 `v1_backend_scheduling: true` 时行为与 v2.1.5 完全一致，任务仍由排插自己执行。

### 实机验证（2026-11-24 01:38~01:41，生产栈 v2.1.6，设备 `a1b2c3d4e5f6` plug_0=插口1）

| 判据 | 实测 |
|---|---|
| 服务器侧建任务 | `PUT /api/admin/device-config/a1b2c3d4e5f6` 写入 `plug_0/task_0 = 01:41、repeat=0、action=1、on=1`，返回 `{"status":"ok"}`，GET 回读一致 |
| 接管 + 停用设备侧副本 | `01:38:47` 日志 `[scheduler] 已接管 v1 任务: a1b2c3d4e5f6 插口0 任务task_0 —— 设备侧副本已停用, 改由后端按服务器时间执行`；`01:38:58` 设备 state 显示 `task_0 = on=0 01:41 rep=0`（字段保留、仅停用） |
| 按服务器时间触发 | `01:41:10` 日志 `[scheduler] 已触发(v1): a1b2c3d4e5f6 插口0 任务task_0 -> {"mac":"a1b2c3d4e5f6","plug_0":{"on":1}}`（整分后 10 秒，落在 15s tick 内） |
| 实际动作 | `01:41:22` 采样到 `plug_0 on=1`，灯亮 —— **端到端通过** |
| 收尾 | 服务器设备配置恢复原样（测试任务已移除）、灯恢复 ON、设备侧任务保持 `on=0`；`/api/ping` 200，`/api/admin/config` 回读 `v1_backend_scheduling=true` |

对照：同一台设备在 v2.1.5 时代（设备自己执行）8 组实验只成功 1 次，且触发时刻与设定分钟差 ~65 秒；
改由后端执行后**一次到位**，触发时刻只受 15 秒 tick 粒度影响。

### 回滚

`v1_backend_scheduling: false` + 重启容器 —— 立即回到"排插自己执行"的老行为；
设备侧任务需在界面上再保存一次才会重新写回设备（否则设备侧仍是 `on=0`）。

## v2.1.5（V1 固件周期校时）

### 为什么发这一版

有朋友的 V1 排插反馈"定时任务经常延期"，判断成"晶振老化 / NTP 连不上"。读代码后确认：

- 仓库里**没有任何 NTP 代码**；V1 排插是纯 MQTT 从机，不上外网，时间的唯一来源是控制台推送的 `setting.time`。
- 前端只在**四个事件点**推时间：打开控制台并连上 MQTT（设备首条 state 到达）、设备离线转在线、
  手点"同步设备时间"、保存倒计时前。**后端从来不推**。
- 后果：控制台页面长期不开时，设备时钟从上次校准起自由走（普通晶振 ±20~50 ppm ≈ 1~2 分钟/月），
  定时任务越来越晚或越来越早，且没有任何东西会纠正它。

### 改动

| 位置 | 改动 |
|------|------|
| `backend/main.go` | 新增 `v1_time_sync_interval_hours`（`*int`：未配置=6，`0`=关闭，上限 168）；`taskScheduler` 提到包级；新增诊断路由 `GET /api/admin/diagnostics/time-sync` |
| `backend/scheduler.go` | 调度器实例兼做 V1 周期校时：启动即校一次，之后每 N 小时向所有 `firmware != v2` 的设备各发 `{"mac":..,"setting":{"time":<unix>}}` 到 `device/ztc1/<mac>/set`；只写 `setting.time`，不动插口开关；`firmware` 未标注的按 v1 处理（与前端 `getDeviceVersion` 默认值一致） |
| `backend/config.yaml` | 写明 `v1_time_sync_interval_hours: 6` 及回滚方式 |
| `backend/time_sync_test.go` | 新增 6 组用例：payload 形状、6 小时节流（含时钟回拨不校）、间隔夹取、只挑 v1 设备（大小写去重/非法键跳过/v2 排除）、ACL 缺失识别、诊断接口 200/503 |

**V2 设备完全不受影响**：它们的任务由后端调度器按服务器时间执行，与设备时钟无关，因此不校时。

### 排查手段（给朋友用）

1. 容器日志：每轮一行 `[timesync] 已向 N 台 V1 设备推送校时 <时间> (成功 a, 失败 b, 下次约 6 小时 后): <mac列表>`。
2. `GET /api/admin/diagnostics/time-sync`（需管理员 token）：
   `enabled` / `interval_hours` / `last_sync` / `next_sync` / `last_targets` / `current_targets` / `missing_acl`。
3. **ACL 静默丢包告警**：mosquitto 对无授权 topic 的发布是静默丢弃（不报错）。若管理员 MQTT 账号在 ACL 里
   没有某台设备的 `device/ztc1/<mac>/#`，日志会点名列出，并提示去"管理页 → 用户 → 追加 MAC"补授权。

### 真机实测（2026-11-23 22:22~22:44，生产设备 `a1b2c3d4e5f6`，只动 plug_0=插口1）

| 实验 | 结果 |
|------|------|
| 推 `setting.time`（payload 与代码逐字节一致） | 发布成功，设备回 `setting:{}` 确认；但设备 state **不含时钟字段**，无法回读核对 |
| plug_0 写 `task_4` 一次任务（分两分钟 / 整插口写全 5 个任务 两种形状都试） | 固件接受并回显；设定 22:38 的那次在 22:37:40 左右被固件清空 |
| plug_0 关灯 → 写 `task_0`=22:43 `action=1` → 观察 | **灯在 22:44:05~22:44:15 亮起**，任务行随后被固件清空（一次性任务执行后自清除） |

**由此得到两条与"延期"直接相关的结论**：

1. 固件确实执行 MQTT 写入的定时任务，但实际动作**比设定分钟晚约 60 秒**（本次测得 65~75 秒）。
   这是固件自身的分钟级检查粒度，**不是 NTP、也不是晶振** —— 朋友说的"经常延期（一分钟级）"可以用它解释。
2. 校时只能靠行为验证，不能靠回读。本功能把"时钟漂移"的上限从"上次有人开网页至今"压到固定 6 小时：
   按 ±20~50 ppm 计算，6 小时误差 0.43~1.08 秒，相比"几周不校准"的分钟级误差是数量级改善。

> **2026-11-24 补正（重要）**：上表那次成功**未能复现**——后续 7 组同类实验（含一次性任务对照、原厂 App
> 查询包先行的变体）全部未触发。进一步核查发现：这两台所谓 v1 设备跑的是**原厂闭源固件**
> （设备自报 v1.3.0 / v1.2.1，HTTP 页为原厂"上网设置"页），其时间大概率由固件自身 SNTP 维护，
> 而 `setting.time` **不在原厂协议里**（原厂 Android App 全仓无该载荷）——本功能的实际有效性**未获证实**。
> 完整证据链、协议对照与排障手册见 `docs/固件与定时任务链路.md`。
> 结论修正为：**"v1 定时任务延期"是这一代固件固有短板，不是本项目 bug；`setting.time` 只能算尽力而为**。

### 遗留（未在本版处理，需要再验一轮）

- 实验中 `action=0` 的一次性任务被执行并清空，但当时开着的插口**没有断电** → 该固件的 `action` 语义
  未必是注释里写的"1=开 0=关"，与前端"定时关"是否真的生效需要单独确认。
- 实验残留在 plug_0 留下 5 个 `on:0` 的停用任务行（无副作用，界面可见），如需彻底清空要在设备侧再写一次空任务。

### 回滚

`v1_time_sync_interval_hours: 0` —— 行为回到 v2.1.4（只有前端事件推送），其余逻辑不变。

## v2.1.4（前端构建 Node 升级，本地未部署）

| 位置 | 改动 |
|------|------|
| `frontend/Dockerfile` | builder `node:20-alpine` → **`node:24.22.1-alpine`**（2026-10 的**最新 LTS 线 Krypton**；v26 是 Current、还没转 LTS，构建镜像不追 Current）。标签存在性用 `docker pull` 实测过 |
| `docker-compose.yaml` / `App.vue` | 镜像 tag 与页面版本号 → v2.1.4 |

**JS 依赖一行没动**（`package.json` / `package-lock.json` 未改），所以这一版只换"用什么 Node 打包"。

### 本机预验（不拿 CI 当试错）

- `node -v` = v24.22.1、`npm -v` = 11.19.1：先在 `D:/TMP/node24` 解压官方压缩包用**现有 node_modules** 跑 `npm run build` → 通过。
- 再**复刻 CI 的路径**：另建目录只带 `package.json`/`package-lock.json`/`vite.config.js`/`index.html`/`src`，`npm ci --registry=https://registry.npmmirror.com` + `npm run build` → 通过。
  npm 11 会提示 `esbuild@0.21.5` 的 postinstall 未被 allowScripts 覆盖 —— 不影响产出（esbuild 的平台二进制走 optionalDependencies），构建正常出 dist。

### 顺带量到的前端依赖体检（**本次没修**，留给你决定）

`npm audit`（官方 registry）：**全量 10 条（1 moderate + 9 high）**，涉及 `@vue/server-renderer`、`esbuild`、`ip-address`、`nanoid`、`postcss`、`rollup`、`source-map-js`、`ws`；其中 `--omit=dev` 还剩 **7 high**，全部归到 `ws`（mqtt.js 带进来的传递依赖）。

⚠️ 但这 7 条**不在浏览器产物里执行**：我在构建出的 `dist/assets/*.js` 里搜 `ws` 独有的错误串（`WebSocket was closed before` / `Invalid WebSocket frame` / `permessage-deflate`）**0 命中**，并对同一批串在 `node_modules/ws/lib` 做**阳性对照**（2/3 命中，证明检索方法有效）。
⇒ 属"装在你/构建机的 node_modules 里"的风险，不是页面运行时的风险。要收掉就单独发一版跑 `npm audit fix`（非 --force）；`vite 5 → 6/7` 是破坏性升级，另议。

### 验证

| 判据 | 读数 |
|------|------|
| 本机 node24 + npm11 构建（含复刻 CI 的 `npm ci`） | 通过 |
| `node:24.22.1-alpine` 镜像标签 | NAS 上 `docker pull` 成功（验证用镜像随即删除） |
| CI 出的 v2.1.4 nginx 镜像 | 见下方发布记录：页面版本串与 `umami` 命中数复算 |

### 回滚

`frontend/Dockerfile` 的 FROM 改回 `node:20-alpine`（一行）；或 compose tag 回 `v2.1.3`。

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
| 调度器经 **paho v1.5.1** 真实发消息 | broker 侧 `mosquitto_sub` 抓到 `device/ztc1/set set socket C3D4E5F6A7B8 1 1`（不看我们的日志，看线上真发了什么） |
| 采集器经 v1.5.1 收 + **sqlite v1.61** 写 | 灌两条设备状态 → `power_samples=2`、`task_logs=1`（翻转 1→0）、`scheduler_fires=1`（30s 内多个 tick 只触发一次） |
| **打开你现有的生产库文件**（由旧驱动写的 22 MB 库，含 -wal/-shm 一起复制） | 前后 `pragma integrity_check=ok`；新程序写入后 `power_samples 210488→210489`、`agg_1min 73482→73483`、`scheduler_fires 10→11`，无报错、无迁移异常；生产原件只被复制未被写 |
| `go build` / `go vet` / `go test`（go1.27.3） | 全部通过 |
| **阿里云下来的镜像本身**（`docker create` + `docker cp /app/mqtt_mgr` 后 `go version -m`） | 内嵌 `go1.27.3`，链接的正是 paho **v1.5.1** / websocket **v1.5.3** / x/net **v0.61.0** / x/crypto **v0.58.0** / sqlite **v1.61.0** |
| 对那个**出厂二进制**跑 `govulncheck -mode=binary` | `No vulnerabilities found.`（0 可达；模块级仍剩 openpgp "不再维护"那条，`Fixed in: N/A`） |
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
| NAS 一次性 broker 实跑（`/tmp/tc1_proof`，与生产隔离，跑完已清） | `catchup=30`：6 分钟前的任务出现 `已补发 … 迟到 6 分钟 -> set socket C3D4E5F6A7B8 1 1`，`scheduler_fires` 恰 1 行（去重生效）；`catchup=0`：0 次下发、0 行 |
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
3. 备份：`/srv/tc1/backup_v210/`
4. 浏览器需 **Ctrl+F5** 强刷前端缓存。
5. 本地 `docker-compose.yaml` tag 已同步 v2.1.0。
