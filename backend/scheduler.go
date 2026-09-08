package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
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
// 同一分钟内同一任务只触发一次; 后端重启后从 device_config 重建, 无状态丢失。
// 一次性任务(repeat=0): 配置变更后按其 hour:minute 在"当天"到点触发一次,
// 触发后由 fires 表去重, 不会在同一天反复执行(与固件执行后不停用的行为对齐,
// 前端已有"倒计时到期自动停用 task_4"的清理机制)。

const schedulerTickInterval = 15 * time.Second

type TaskScheduler struct {
	client  mqtt.Client
	store   *PowerStore
	stop    chan struct{}
	stopped chan struct{}
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
	log.Println("[scheduler] 定时调度器已启动 (仅对 firmware=v2 的设备生效)")
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
	for {
		select {
		case <-ticker.C:
			s.tick()
		case <-s.stop:
			return
		}
	}
}

// taskSpec 调度视角的任务
type taskSpec struct {
	mac     string // 小写无冒号
	plugIdx int
	taskKey string
	hour    int
	minute  int
	repeat  int
	action  int
}

// loadV2ScheduledTasks 读取管理员账号下 firmware=v2 设备的启用任务。
func loadV2ScheduledTasks() ([]taskSpec, error) {
	configs, err := getAdminDeviceConfig()
	if err != nil {
		return nil, err
	}
	var out []taskSpec
	for rawMac, cfg := range configs {
		if cfg.Firmware != "v2" {
			continue
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
				})
			}
		}
	}
	return out, nil
}

// tick 每个调度周期: 找出"当前分钟匹配"且"本分钟未触发过"的任务并下发。
func (s *TaskScheduler) tick() {
	// 东八区与固件/前端任务语义一致
	loc := time.FixedZone("CST", 8*3600)
	now := time.Now().In(loc)
	fireMinute := now.Format("2006-01-02 15:04")
	// 星期: 周一=bit0 .. 周日=bit6
	bit := (int(now.Weekday()) + 6) % 7

	tasks, err := loadV2ScheduledTasks()
	if err != nil {
		log.Printf("[scheduler] 读取任务配置失败: %v", err)
		return
	}
	for _, t := range tasks {
		if t.hour != now.Hour() || t.minute != now.Minute() {
			continue
		}
		if t.repeat != 0 && t.repeat&(1<<bit) == 0 {
			continue // 周期任务: 今天不执行
		}
		if s.store.HasSchedulerFire(t.mac, t.plugIdx, t.taskKey, fireMinute) {
			continue // 本分钟已触发过(含重启后的重复 tick)
		}
		s.store.MarkSchedulerFire(t.mac, t.plugIdx, t.taskKey, fireMinute)

		payload := fmt.Sprintf("set socket %s %d %d", strings.ToUpper(t.mac), t.plugIdx, t.action)
		token := s.client.Publish("device/ztc1/set", 0, false, payload)
		go func(ts taskSpec, p string) {
			if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
				log.Printf("[scheduler] 下发失败 %s 插口%d %s: %v", ts.mac, ts.plugIdx, ts.taskKey, token.Error())
				return
			}
			log.Printf("[scheduler] 已触发: %s 插口%d 任务%s -> %s", ts.mac, ts.plugIdx, ts.taskKey, p)
		}(t, payload)
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
