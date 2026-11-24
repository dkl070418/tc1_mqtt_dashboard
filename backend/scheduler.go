package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// V2 设备定时任务后端调度器 (方案A)。
//
// 背景: v2 固件的 MQTT 命令路径(ProcessHaCmd)没有定时配置命令, 定时引擎
// (timed_task.c)只能经设备自带 HTTP 服务配置。为了让 v2 设备在控制台获得
// 与 v1 一致的定时体验, 定时执行由后端代劳: 读 device_config 的 tasks,
// 到点经 MQTT 向 `device/ztc1/set` 发 `set socket <大写MAC> <idx> <on>`。
//
// 任务语义与前端 TaskEditor 一致:
//   on=1 启用; repeat 位掩码 bit0=周一..bit6=周日, 0=一次性; action 1=开 0=关。
//
// 防重复: sqlite 表 scheduler_fires 记录 (mac, plug, task, fire_minute),
// fire_minute 取任务"应执行时刻"(而非实际下发时刻), 所以补发/重试都不会重复触发;
// 同一分钟同一任务只触发一次; 后端重启后从 device_config 重建, 无状态丢失。
// 一次性任务(repeat=0): 配置变更后按其 hour:minute 在"当天"到点触发一次,
// 触发后由 fires 表去重, 不会在同一天反复执行(与固件执行后不停用的行为对齐,
// 前端已有"倒计时到期自动停用 task_4"的清理机制)。
//
// 错过补发: 配置项 scheduler_catchup_minutes(默认 30, 0=关闭)。后端在那一分钟
// 不活着(容器重启/NAS 睡眠/发送失败)时, 窗口内后续 tick 会补发, 日志标"补发"。
// 窗口 = 1 分钟时判定与旧版"只认当前分钟"逐条等价(见 scheduler_test.go)。

const schedulerTickInterval = 15 * time.Second

type TaskScheduler struct {
	client  mqtt.Client
	store   *PowerStore
	stop    chan struct{}
	stopped chan struct{}

	// V1 周期校时状态（见文件末尾 v1 校时一节）
	timeSyncMu   sync.Mutex
	lastTimeSync time.Time
	lastSync     timeSyncResult

	// V1 后端接管状态（adoptV1Tasks）
	adoptMu     sync.Mutex
	adoptAt     map[string]time.Time
	adoptLogged map[string]bool
}

func NewTaskScheduler(store *PowerStore) *TaskScheduler {
	return &TaskScheduler{
		store:   store,
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

// Start 建立 MQTT 连接并启动调度循环。
func (s *TaskScheduler) Start(broker, username, password string) error {
	opts := mqtt.NewClientOptions()
	opts.AddBroker(broker)
	opts.SetClientID(fmt.Sprintf("task-scheduler-%d", time.Now().UnixNano()))
	opts.SetUsername(username)
	opts.SetPassword(password)
	opts.SetCleanSession(true)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(5 * time.Second)
	opts.SetMaxReconnectInterval(30 * time.Second)
	opts.SetConnectionLostHandler(func(c mqtt.Client, err error) {
		log.Printf("[scheduler] MQTT 连接断开: %v", err)
	})

	client := mqtt.NewClient(opts)
	token := client.Connect()
	if !token.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("MQTT 连接超时")
	}
	if token.Error() != nil {
		return fmt.Errorf("MQTT 连接失败: %w", token.Error())
	}
	s.client = client

	go s.loop()
	if catchup := schedulerCatchupMinutes(); catchup > 0 {
		log.Printf("[scheduler] 定时调度器已启动 (仅对 firmware=v2 的设备生效), 错过补发窗口=%d 分钟", catchup)
	} else {
		log.Printf("[scheduler] 定时调度器已启动 (仅对 firmware=v2 的设备生效), 错过补发已关闭(只认当前分钟)")
	}
	if h := v1TimeSyncIntervalHours(); h > 0 {
		log.Printf("[timesync] V1 固件周期校时已启用: 每 %d 小时向 firmware!=v2 的设备推送一次 setting.time (启动即校一次)", h)
	} else {
		log.Printf("[timesync] V1 固件周期校时已关闭(v1_time_sync_interval_hours=0), 仅保留前端事件推送")
	}
	return nil
}

func (s *TaskScheduler) Stop() {
	close(s.stop)
	<-s.stopped
}

func (s *TaskScheduler) loop() {
	defer close(s.stopped)
	ticker := time.NewTicker(schedulerTickInterval)
	defer ticker.Stop()
	// 启动即校一次 v1 时间：容器刚起来时设备时钟可能已经漂了很久
	s.maybeSyncV1Times(time.Now(), "启动")
	for {
		select {
		case <-ticker.C:
			s.tick()
			s.maybeSyncV1Times(time.Now(), "周期")
		case <-s.stop:
			return
		}
	}
}

// taskSpec 调度视角的任务
type taskSpec struct {
	mac      string // 小写无冒号
	plugIdx  int
	taskKey  string
	hour     int
	minute   int
	repeat   int
	action   int
	firmware string // "v1" | "v2"：决定下发形态
}

// taskExec 任务的执行位置：
//   - "server"：后端按服务器时间执行（同时设备侧副本要停用）
//   - 其它（缺省/"device"）：仍由排插自己执行，后端不碰
//
// v1 设备缺省是 device（保持与原厂 App 一致的行为）；v2 没有设备侧任务通道，调用方按 server 处理。
func taskExec(m map[string]any) string {
	if s, ok := m["exec"].(string); ok && s == "server" {
		return "server"
	}
	return "device"
}

// loadScheduledTasks 读取管理员账号下"由后端执行"的启用任务。
//   - firmware=v2：始终由后端执行（zTC1 固件没有 MQTT 任务命令，见 PROJECT_HANDOFF 第零节）
//   - firmware!=v2（原厂 v1）：只有任务带 exec="server" 且总闸 v1_backend_scheduling 打开时才纳入；
//     其余任务保持"排插自己执行"，后端既不调度也不改动（安卓 App 建的任务因此完全不受影响）
func loadScheduledTasks() ([]taskSpec, error) {
	configs, err := getAdminDeviceConfig()
	if err != nil {
		return nil, err
	}
	v1Enabled := cfg.V1BackendScheduling
	var out []taskSpec
	for rawMac, cfg := range configs {
		fw := "v1"
		if cfg.Firmware == "v2" {
			fw = "v2"
		} else if !v1Enabled {
			continue // 总闸关闭：v1 全部维持设备执行
		}
		mac := normalizeDeviceMAC(rawMac)
		if !deviceConfigPattern.MatchString(mac) {
			continue
		}
		for plugIdxStr, tasksMap := range cfg.Tasks {
			plugIdx, err := strconv.Atoi(plugIdxStr)
			if err != nil || plugIdx < 0 || plugIdx >= 6 {
				continue
			}
			for taskKey, raw := range tasksMap {
				m, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				on, _ := anyToInt(m["on"])
				if on != 1 {
					continue
				}
				// v1 只接管显式声明 exec=server 的任务
				if fw == "v1" && taskExec(m) != "server" {
					continue
				}
				hour, ok1 := anyToInt(m["hour"])
				minute, ok2 := anyToInt(m["minute"])
				repeat, _ := anyToInt(m["repeat"])
				action, ok3 := anyToInt(m["action"])
				if !ok1 || !ok2 || !ok3 {
					continue
				}
				if hour < 0 || hour > 23 || minute < 0 || minute > 59 || action < 0 || action > 1 {
					continue
				}
				out = append(out, taskSpec{
					mac: mac, plugIdx: plugIdx, taskKey: taskKey,
					hour: hour, minute: minute, repeat: repeat, action: action,
					firmware: fw,
				})
			}
		}
	}
	return out, nil
}

// loadV2ScheduledTasks 兼容旧调用点/测试：只返回 v2 任务
func loadV2ScheduledTasks() ([]taskSpec, error) {
	all, err := loadScheduledTasks()
	if err != nil {
		return nil, err
	}
	out := make([]taskSpec, 0, len(all))
	for _, t := range all {
		if t.firmware == "v2" {
			out = append(out, t)
		}
	}
	return out, nil
}

// v1SwitchPayload 原厂固件的开关命令（与前端 DeviceControl 发出的完全同构）
func v1SwitchPayload(mac string, plugIdx, on int) string {
	return fmt.Sprintf(`{"mac":%q,"plug_%d":{"on":%d}}`, mac, plugIdx, on)
}

// v1TimeSyncPayload 之外，v1 还需要"停用设备侧同名任务"的载荷
func v1DisableTaskPayload(t taskSpec) string {
	return fmt.Sprintf(
		`{"mac":%q,"plug_%d":{"setting":{%q:{"hour":%d,"minute":%d,"repeat":%d,"action":%d,"on":0}}}}`,
		t.mac, t.plugIdx, t.taskKey, t.hour, t.minute, t.repeat, t.action)
}

// dueOccurrence 返回任务在 [now-window, now] 内最近一次"应执行时刻"。
// 回看昨天只为覆盖 23:5x 的任务在零点后补发; 更早的时刻由窗口本身挡掉。
func dueOccurrence(t taskSpec, now time.Time, window time.Duration) (time.Time, bool) {
	for dayOffset := 0; dayOffset <= 1; dayOffset++ {
		day := now.AddDate(0, 0, -dayOffset)
		if t.repeat != 0 {
			// 星期: 周一=bit0 .. 周日=bit6
			if t.repeat&(1<<((int(day.Weekday())+6)%7)) == 0 {
				continue
			}
		}
		occ := time.Date(day.Year(), day.Month(), day.Day(), t.hour, t.minute, 0, 0, now.Location())
		if late := now.Sub(occ); late >= 0 && late < window {
			return occ, true
		}
	}
	return time.Time{}, false
}

// tick 每个调度周期: 找出"补发窗口内到期"且"该分钟未触发过"的任务并下发。
func (s *TaskScheduler) tick() {
	// 东八区与固件/前端任务语义一致
	loc := time.FixedZone("CST", 8*3600)
	now := time.Now().In(loc)
	window := time.Minute + time.Duration(schedulerCatchupMinutes())*time.Minute

	tasks, err := loadScheduledTasks()
	if err != nil {
		log.Printf("[scheduler] 读取任务配置失败: %v", err)
		return
	}
	s.adoptV1Tasks(tasks, now)
	for _, t := range tasks {
		occ, due := dueOccurrence(t, now, window)
		if !due {
			continue
		}
		// fire_minute 记"应执行时刻", 补发与重试都不会与按时触发撞车
		fireMinute := occ.Format("2006-01-02 15:04")
		if s.store.HasSchedulerFire(t.mac, t.plugIdx, t.taskKey, fireMinute) {
			continue // 该分钟已触发过(含重启后的重复 tick)
		}

		// v2: 共享 topic + 文本命令(大写 MAC)；v1: 每设备 topic + JSON(小写 MAC)
		var topic, payload, tag string
		if t.firmware == "v2" {
			topic = "device/ztc1/set"
			payload = fmt.Sprintf("set socket %s %d %d", strings.ToUpper(t.mac), t.plugIdx, t.action)
			tag = ""
		} else {
			topic = fmt.Sprintf("device/ztc1/%s/set", t.mac)
			payload = v1SwitchPayload(t.mac, t.plugIdx, t.action)
			tag = "(v1)"
		}
		// 先发布成功再记 fire：失败不落库，窗口内后续 tick(约15s)可重试
		token := s.client.Publish(topic, 0, false, payload)
		ok := token.WaitTimeout(5*time.Second) && token.Error() == nil
		if !ok {
			errMsg := "timeout"
			if token.Error() != nil {
				errMsg = token.Error().Error()
			}
			log.Printf("[scheduler] 下发失败(窗口内将重试) %s 插口%d %s: %s", t.mac, t.plugIdx, t.taskKey, errMsg)
			continue
		}
		s.store.MarkSchedulerFire(t.mac, t.plugIdx, t.taskKey, fireMinute)
		if late := now.Sub(occ); late >= time.Minute {
			log.Printf("[scheduler] 已补发%s: %s 插口%d 任务%s 应执行 %s (迟到 %d 分钟) -> %s",
				tag, t.mac, t.plugIdx, t.taskKey, fireMinute, int(late.Minutes()), payload)
		} else {
			log.Printf("[scheduler] 已触发%s: %s 插口%d 任务%s -> %s", tag, t.mac, t.plugIdx, t.taskKey, payload)
		}
	}
}

// adoptV1Tasks 让 v1 设备"交出"定时任务：把设备侧同名任务写 on:0，
// 避免固件按自己那个不准的时钟再触发一次（双触发）。
//
// 为什么要反复做：前端保存任务时仍会写一次设备（on=1），所以每台设备每 10 分钟
// 复查一次、发现又开着就再停用；日志只在首次接管时打一行，避免刷屏。
func (s *TaskScheduler) adoptV1Tasks(tasks []taskSpec, now time.Time) {
	if !cfg.V1BackendScheduling || len(tasks) == 0 {
		return
	}
	s.adoptMu.Lock()
	defer s.adoptMu.Unlock()
	if s.adoptAt == nil {
		s.adoptAt = make(map[string]time.Time)
		s.adoptLogged = make(map[string]bool)
	}
	for _, t := range tasks {
		if t.firmware != "v1" {
			continue
		}
		key := fmt.Sprintf("%s|%d|%s", t.mac, t.plugIdx, t.taskKey)
		// 复查间隔取 1 分钟：前端保存任务时仍会写一次设备(on=1)，这里尽快把它停回去，
		// 把"设备侧还开着的窗口"压到 ≤1 分钟（每次只有一条极小的 MQTT 消息）。
		if last, ok := s.adoptAt[key]; ok && now.Sub(last) < time.Minute {
			continue
		}
		topic := fmt.Sprintf("device/ztc1/%s/set", t.mac)
		token := s.client.Publish(topic, 0, false, v1DisableTaskPayload(t))
		if !token.WaitTimeout(3*time.Second) || token.Error() != nil {
			continue // 下一轮 tick 再试
		}
		s.adoptAt[key] = now
		if !s.adoptLogged[key] {
			s.adoptLogged[key] = true
			log.Printf("[scheduler] 已接管 v1 任务: %s 插口%d 任务%s —— 设备侧副本已停用, 改由后端按服务器时间执行",
				t.mac, t.plugIdx, t.taskKey)
		}
	}
}

// SchedulerFireDiagnostics 供调试/验证使用 (JSON 输出最近触发记录)
func SchedulerFireDiagnostics(store *PowerStore) string {
	rows, err := store.RecentSchedulerFires(20)
	if err != nil {
		return err.Error()
	}
	b, _ := json.MarshalIndent(rows, "", "  ")
	return string(b)
}

// ---- V1 固件周期校时 ----
//
// 为什么要后端做: v1 固件的定时任务由**设备自己的时钟**执行, 而排插不联网、不跑 NTP,
// 时间只能由控制台通过 `setting.time` 推过去。前端只在四个事件点推:
// 打开控制台并连上 MQTT / 设备离线转在线 / 手点"同步设备时间" / 保存倒计时前。
// 页面长期不开(或只用手机看一眼就关)时, 设备时钟从上次校准起自由漂移 —— 普通晶振
// ±20~50ppm 约 1~2 分钟/月 —— 表现为定时任务越来越晚或越来越早。
//
// 这里做兜底: 每 v1_time_sync_interval_hours 小时(默认 6, 0=关闭)向所有
// firmware!=v2 的设备各推一次 `{"mac":..,"setting":{"time":<unix>}}`。
// topic 与 payload 和前端完全一致(device/ztc1/<mac>/set), 因此不改变任何既有行为;
// 只写 setting.time, 不动插口开关, 也不碰 v2 设备(它们的任务由本调度器按服务器时间执行)。

const (
	defaultV1TimeSyncIntervalHours = 6
	v1TimeSyncMaxIntervalHours     = 168 // 7 天
)

// cstZone 与调度器/前端任务语义一致的东八区
var cstZone = time.FixedZone("CST", 8*3600)

// v1TimeSyncMsg 与前端 emit('send-cmd', {mac, setting:{time}}) 发出的 JSON 同构
type v1TimeSyncMsg struct {
	MAC     string           `json:"mac"`
	Setting map[string]int64 `json:"setting"`
}

// v1TimeSyncPayload 构造校时 payload。
func v1TimeSyncPayload(mac string, unix int64) string {
	b, err := json.Marshal(v1TimeSyncMsg{MAC: mac, Setting: map[string]int64{"time": unix}})
	if err != nil {
		return ""
	}
	return string(b)
}

// timeSyncDue 判断本轮是否该校时。last 为零值(刚启动)时立即校一次;
// now 早于 last(系统时间被回拨)时不校, 以免连续轰炸设备。
func timeSyncDue(last, now time.Time, interval time.Duration) bool {
	if interval <= 0 {
		return false
	}
	if last.IsZero() {
		return true
	}
	return !now.Before(last.Add(interval))
}

// loadV1Devices 取需要校时的设备 MAC: firmware=v2 跳过; 未标注 firmware 的按 v1 处理
// (与前端 getDeviceVersion 的默认值一致, 避免老配置漏校)。
func loadV1Devices() ([]string, error) {
	configs, err := getAdminDeviceConfig()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(configs))
	out := make([]string, 0, len(configs))
	for rawMac, c := range configs {
		if c.Firmware == "v2" {
			continue
		}
		mac := normalizeDeviceMAC(rawMac)
		if !deviceConfigPattern.MatchString(mac) {
			continue
		}
		if _, dup := seen[mac]; dup {
			continue
		}
		seen[mac] = struct{}{}
		out = append(out, mac)
	}
	sort.Strings(out)
	return out, nil
}

// missingTimeSyncACL 返回管理员 MQTT 账号在 ACL 里没有授权的设备。
// mosquitto 对无 ACL 的发布是**静默丢弃**(不报错), 所以这是"发了但没生效"的唯一线索。
func missingTimeSyncACL(macs []string) []string {
	if len(macs) == 0 {
		return nil
	}
	allowed, err := getUserDeviceMACs(cfg.AdminUsername)
	if err != nil {
		return nil // 读不到 ACL(文件不存在等)时不误报, 交给发布结果说话
	}
	has := make(map[string]struct{}, len(allowed))
	for _, m := range allowed {
		has[m] = struct{}{}
	}
	var missing []string
	for _, m := range macs {
		if _, ok := has[m]; !ok {
			missing = append(missing, m)
		}
	}
	return missing
}

// timeSyncResult 一轮校时的结果(供日志与诊断接口)
type timeSyncResult struct {
	At         time.Time `json:"-"`
	Reason     string    `json:"reason"`
	Targets    []string  `json:"targets"`
	OK         int       `json:"ok"`
	Failed     int       `json:"failed"`
	MissingACL []string  `json:"missing_acl,omitempty"`
}

// maybeSyncV1Times 到点才真正下发(按 v1_time_sync_interval_hours 节流)
func (s *TaskScheduler) maybeSyncV1Times(now time.Time, reason string) {
	interval := time.Duration(v1TimeSyncIntervalHours()) * time.Hour
	if interval <= 0 {
		return
	}
	s.timeSyncMu.Lock()
	last := s.lastTimeSync
	s.timeSyncMu.Unlock()
	if !timeSyncDue(last, now, interval) {
		return
	}
	s.syncV1Times(now, reason, interval)
}

// syncV1Times 向所有 v1 设备推送当前时间(Unix 秒)
func (s *TaskScheduler) syncV1Times(now time.Time, reason string, interval time.Duration) {
	macs, err := loadV1Devices()
	if err != nil {
		log.Printf("[timesync] 读取设备配置失败, 本轮跳过: %v", err)
		return
	}
	if len(macs) == 0 {
		// 不记时间戳: 之后新加 v1 设备能在下一个 tick(15s)就被校到
		log.Printf("[timesync] 无 firmware=v1 的设备, 本轮无需校时")
		return
	}

	res := timeSyncResult{At: now, Reason: reason, Targets: macs, MissingACL: missingTimeSyncACL(macs)}
	unix := now.Unix()
	for _, mac := range macs {
		topic := fmt.Sprintf("device/ztc1/%s/set", mac)
		token := s.client.Publish(topic, 0, false, v1TimeSyncPayload(mac, unix))
		if token.WaitTimeout(3*time.Second) && token.Error() == nil {
			res.OK++
			continue
		}
		res.Failed++
		msg := "timeout"
		if token.Error() != nil {
			msg = token.Error().Error()
		}
		log.Printf("[timesync] 下发失败 %s -> %s: %s", mac, topic, msg)
	}

	s.timeSyncMu.Lock()
	s.lastTimeSync = now
	s.lastSync = res
	s.timeSyncMu.Unlock()

	log.Printf("[timesync] 已向 %d 台 V1 设备推送校时 %s (成功 %d, 失败 %d, 下次约 %s 后): %s",
		len(macs), now.In(cstZone).Format("2006-01-02 15:04:05"), res.OK, res.Failed,
		formatSyncInterval(interval), strings.Join(macs, ","))
	if len(res.MissingACL) > 0 {
		log.Printf("[timesync] 警告: 管理员账号 %s 缺少这些设备的 ACL, mosquitto 会静默丢弃校时: %s (管理页 → 用户 → 追加 MAC 可授权)",
			cfg.AdminUsername, strings.Join(res.MissingACL, ","))
	}
}

func formatSyncInterval(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%d 天", int(d/(24*time.Hour)))
	}
	return fmt.Sprintf("%g 小时", d.Hours())
}

// TimeSyncDiagnostics 供排查: 上次/下次校时、目标设备、成功失败、缺 ACL 的设备
func (s *TaskScheduler) TimeSyncDiagnostics() string {
	intervalHours := v1TimeSyncIntervalHours()
	s.timeSyncMu.Lock()
	last, res := s.lastTimeSync, s.lastSync
	s.timeSyncMu.Unlock()

	out := map[string]any{
		"enabled":        intervalHours > 0,
		"interval_hours": intervalHours,
	}
	if !last.IsZero() {
		out["last_sync"] = last.In(cstZone).Format("2006-01-02 15:04:05")
		out["last_reason"] = res.Reason
		out["last_targets"] = res.Targets
		out["last_ok"] = res.OK
		out["last_failed"] = res.Failed
		if len(res.MissingACL) > 0 {
			out["missing_acl"] = res.MissingACL
		}
		if intervalHours > 0 {
			out["next_sync"] = last.Add(time.Duration(intervalHours) * time.Hour).In(cstZone).Format("2006-01-02 15:04:05")
		}
	}
	if macs, err := loadV1Devices(); err == nil {
		out["current_targets"] = macs
	} else {
		out["error"] = err.Error()
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err.Error()
	}
	return string(b)
}

// handleTimeSyncDiagnostics /api/admin/diagnostics/time-sync
func handleTimeSyncDiagnostics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if taskScheduler == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"调度器未启动: 检查 power_collect_enabled 与启动日志"}`))
		return
	}
	w.Write([]byte(taskScheduler.TimeSyncDiagnostics()))
}
