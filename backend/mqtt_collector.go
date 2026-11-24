package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type PowerCollector struct {
	store  *PowerStore
	client mqtt.Client
	done   chan struct{}
	mu     sync.Mutex
	lastTS map[string]int64          // mac -> 上次采样时间，用于计算积分间隔
	gaps   map[string][]float64      // mac -> 最近若干个采样间隔，用于自适应积分上限
	plugOn map[string]map[int]int    // mac -> (插口索引 -> 上次 on 状态)，用于检测状态翻转
}

func NewPowerCollector(store *PowerStore) *PowerCollector {
	return &PowerCollector{
		store:  store,
		done:   make(chan struct{}),
		lastTS: make(map[string]int64),
		gaps:   make(map[string][]float64),
		plugOn: make(map[string]map[int]int),
	}
}

// gapHistoryLen 自适应上限用多少个历史间隔来取中位数
const gapHistoryLen = 10

// medianFloat 返回中位数（空切片返回 0）
func medianFloat(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	cp := append([]float64(nil), v...)
	sort.Float64s(cp)
	n := len(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return (cp[n/2-1] + cp[n/2]) / 2
}

// integralInterval 计算"本次采样代表多长时间"，用于能量积分。
//
// 早期实现把间隔硬砍到 30 秒（防断线重连后的巨大间隔），但这对"上报本来就稀疏"的设备是灾难：
// V2 固件的功率流在 homeassistant/sensor/<MAC>/power/state（默认 2 秒一条），
// 一旦某台设备的上报周期是分钟级，硬砍会让用电量低估成百上千倍。
//
// 现在改成自适应：上限 = max(30s, 3 × 该设备最近间隔的中位数)，再叠一个 1 小时的天花板。
//   - 2 秒级上报（V1/V2 正常情况）→ 上限仍是 30 秒，行为与旧版一致
//   - 分钟级上报 → 上限随之放大，能量不再被系统性低估
//   - 断线重连 / 设备消失很久 → 仍被挡住（最多按 1 小时计）
func integralInterval(gapS float64, history []float64) float64 {
	if gapS < 0.5 {
		return 0.5
	}
	capS := 30.0
	if m := medianFloat(history); m > 0 {
		if v := 3 * m; v > capS {
			capS = v
		}
	}
	if capS > 3600 {
		capS = 3600
	}
	if gapS > capS {
		return capS
	}
	return gapS
}

func (pc *PowerCollector) Start(broker, username, password string) error {
	opts := mqtt.NewClientOptions()
	opts.AddBroker(broker)
	opts.SetClientID(fmt.Sprintf("power-collector-%d", time.Now().UnixNano()))
	opts.SetUsername(username)
	opts.SetPassword(password)
	opts.SetCleanSession(true)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(5 * time.Second)
	opts.SetMaxReconnectInterval(30 * time.Second)
	opts.SetConnectionLostHandler(func(c mqtt.Client, err error) {
		log.Printf("MQTT 连接断开: %v", err)
	})
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		log.Println("MQTT 连接成功，开始订阅设备主题")
		pc.subscribeDevices(c)
	})

	client := mqtt.NewClient(opts)
	token := client.Connect()
	if !token.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("MQTT 连接超时")
	}
	if token.Error() != nil {
		return fmt.Errorf("MQTT 连接失败: %w", token.Error())
	}
	pc.client = client
	return nil
}

func (pc *PowerCollector) subscribeDevices(c mqtt.Client) {
	macs, err := getUserDeviceMACs(cfg.AdminUsername)
	if err != nil {
		log.Printf("获取设备列表失败: %v", err)
		time.AfterFunc(30*time.Second, func() { pc.subscribeDevices(c) })
		return
	}
	for _, raw := range macs {
		// MQTT topic 大小写敏感：V1 用小写 MAC，V2 固件用大写 MAC。
		// 双大小写订阅，消息统一归一到小写 MAC 再入库/推 webhook。
		macLower := strings.ToLower(strings.NewReplacer(":", "", "-", "").Replace(raw))
		macUpper := strings.ToUpper(macLower)
		handler := pc.makeHandler(macLower)
		topics := []string{
			fmt.Sprintf("device/ztc1/%s/sensor", macLower),
			fmt.Sprintf("device/ztc1/%s/state", macLower),
			fmt.Sprintf("device/ztc1/%s/sensor", macUpper),
			fmt.Sprintf("device/ztc1/%s/state", macUpper),
		}
		for _, topic := range topics {
			t := c.Subscribe(topic, 0, handler)
			t.WaitTimeout(5 * time.Second)
			if t.Error() != nil {
				log.Printf("订阅 %s 失败: %v", topic, t.Error())
			} else {
				log.Printf("已订阅 %s", topic)
			}
		}
		// V2 设备名/插口名在 HA discovery config 里（state 无顶层 name）
		haHandler := pc.makeHAConfigHandler(macLower)
		for _, topic := range []string{
			fmt.Sprintf("homeassistant/switch/%s/+/config", macUpper),
			fmt.Sprintf("homeassistant/switch/%s/+/config", macLower),
			// V2 固件的实时功率流：每 mqtt_report_freq 秒（默认 2s）一条
			// {"power":"30.098"} —— 之前漏订它，导致 V2 只有"状态变化时"的快照功率，
			// 采样极其稀疏（实测 8 天 24 条），曲线几乎空白、能量严重低估。
			fmt.Sprintf("homeassistant/sensor/%s/power/state", macUpper),
			fmt.Sprintf("homeassistant/sensor/%s/power/state", macLower),
		} {
			h := haHandler
			if strings.Contains(topic, "/power/state") {
				h = handler // 功率流走能耗处理（解析 + 积分入库），不是命名
			}
			t := c.Subscribe(topic, 0, h)
			t.WaitTimeout(5 * time.Second)
			if t.Error() != nil {
				log.Printf("订阅 %s 失败: %v", topic, t.Error())
			} else {
				log.Printf("已订阅 %s", topic)
			}
		}
		// 设备自报的累计电量计数器（today / total / yesterday），仅用于对照显示
		counterHandler := makeCounterHandler(macLower)
		for _, topic := range []string{
			fmt.Sprintf("homeassistant/sensor/%s/powerConsumptionToday/state", macUpper),
			fmt.Sprintf("homeassistant/sensor/%s/powerConsumptionToday/state", macLower),
			fmt.Sprintf("homeassistant/sensor/%s/powerConsumptionYesterday/state", macUpper),
			fmt.Sprintf("homeassistant/sensor/%s/powerConsumptionYesterday/state", macLower),
			fmt.Sprintf("homeassistant/sensor/%s/powerConsumption/state", macUpper),
			fmt.Sprintf("homeassistant/sensor/%s/powerConsumption/state", macLower),
		} {
			t := c.Subscribe(topic, 0, counterHandler)
			t.WaitTimeout(5 * time.Second)
			if t.Error() != nil {
				log.Printf("订阅 %s 失败: %v", topic, t.Error())
			}
		}
	}
}

// makeHAConfigHandler 从 HA discovery config 提取设备名与插口名（V2 用）
func (pc *PowerCollector) makeHAConfigHandler(mac string) mqtt.MessageHandler {
	macLower := strings.ToLower(mac)
	return func(c mqtt.Client, msg mqtt.Message) {
		var payload struct {
			Name   string `json:"name"`
			Device struct {
				Name string `json:"name"`
			} `json:"device"`
		}
		if err := json.Unmarshal(msg.Payload(), &payload); err != nil {
			return
		}
		// topic: homeassistant/switch/<MAC>/socket_N/config 或 .../led/config
		parts := strings.Split(msg.Topic(), "/")
		if len(parts) < 5 {
			return
		}
		entity := parts[len(parts)-2]

		if strings.TrimSpace(payload.Device.Name) != "" {
			flipNames.mu.Lock()
			if flipNames.devices == nil {
				flipNames.devices = make(map[string]string)
			}
			flipNames.devices[macLower] = strings.TrimSpace(payload.Device.Name)
			flipNames.mu.Unlock()
		}

		if strings.HasPrefix(entity, "socket_") && strings.TrimSpace(payload.Name) != "" {
			idx, err := strconv.Atoi(strings.TrimPrefix(entity, "socket_"))
			if err != nil || idx < 0 || idx > 5 {
				return
			}
			flipNames.mu.Lock()
			if flipNames.plugs[macLower] == nil {
				flipNames.plugs[macLower] = make(map[int]string)
			}
			flipNames.plugs[macLower][idx] = strings.TrimSpace(payload.Name)
			flipNames.mu.Unlock()
		}
	}
}

// ---- 设备自报的累计电量（V2 固件自己算的计数器，用于交叉校验）----
//
// V2 固件会发布 powerConsumption（开机至今）、powerConsumptionToday、powerConsumptionYesterday
// 三个 kWh 计数器。本站自己的统计依赖采样完整度（设备换芯片/断线期间会缺），
// 所以把设备自报值一并收进来，前端可以对照显示"设备自报 X kWh"。
var (
	deviceCountersMu sync.Mutex
	deviceCounters   = map[string]map[string]float64{}
)

func setDeviceCounter(mac, key string, v float64) {
	deviceCountersMu.Lock()
	defer deviceCountersMu.Unlock()
	m := deviceCounters[mac]
	if m == nil {
		m = make(map[string]float64, 3)
		deviceCounters[mac] = m
	}
	m[key] = v
}

// DeviceCounters 返回设备自报电量（key: today / total / yesterday，单位 kWh）；没有则 nil
func DeviceCounters(mac string) map[string]float64 {
	deviceCountersMu.Lock()
	defer deviceCountersMu.Unlock()
	src := deviceCounters[mac]
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]float64, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// makeCounterHandler 解析 homeassistant/sensor/<MAC>/powerConsumption*/state 的计数载荷
func makeCounterHandler(mac string) mqtt.MessageHandler {
	fieldToKey := map[string]string{
		"powerConsumptionToday":     "today",
		"powerConsumption":          "total",
		"powerConsumptionYesterday": "yesterday",
	}
	return func(c mqtt.Client, msg mqtt.Message) {
		var payload map[string]any
		if err := json.Unmarshal(msg.Payload(), &payload); err != nil {
			return
		}
		for field, key := range fieldToKey {
			raw, ok := payload[field]
			if !ok {
				continue
			}
			switch v := raw.(type) {
			case string:
				var f float64
				if _, err := fmt.Sscanf(v, "%f", &f); err == nil {
					setDeviceCounter(mac, key, f)
				}
			case float64:
				setDeviceCounter(mac, key, v)
			}
		}
	}
}

// parsePowerW 从一条 JSON 载荷里取功率（V1/V2 都是 W，可能是数字也可能是字符串）。
// 返回 ok=false 表示这条载荷没有功率字段（例如只带插口状态的 state 消息）。
func parsePowerW(payload map[string]any) (float64, bool) {
	raw, exists := payload["power"]
	if !exists {
		return 0, false
	}
	switch v := raw.(type) {
	case float64:
		return v, true
	case string:
		var w float64
		if _, err := fmt.Sscanf(v, "%f", &w); err == nil {
			return w, true
		}
	}
	return 0, false
}

func (pc *PowerCollector) makeHandler(mac string) mqtt.MessageHandler {
	return func(c mqtt.Client, msg mqtt.Message) {
		var payload map[string]any
		if err := json.Unmarshal(msg.Payload(), &payload); err != nil {
			return
		}

		// 缓存设备/插口名，供 Webhook 文案使用
		observeFlipNames(mac, payload)

		// 先记录插口状态翻转（即使功率异常也不遗漏状态变化）
		pc.detectPlugFlips(mac, payload)

		// 没有功率字段的载荷（只带插口状态）到此为止
		powerW, hasPower := parsePowerW(payload)
		if !hasPower {
			return
		}
		if powerW < 0 {
			// 负值功率：疑似设备功耗芯片损坏，不采集统计
			return
		}
		now := time.Now().Unix()
		pc.mu.Lock()
		last, had := pc.lastTS[mac]
		pc.lastTS[mac] = now
		gapHistory := pc.gaps[mac]
		pc.mu.Unlock()

		gap := 2.0 // 默认 2 秒（首次采样或重置用）
		if had && now > last {
			gap = float64(now - last)
		}
		// 自适应上限：见 integralInterval 的注释（V2 功率流 2 秒一条，稀疏源不再被硬砍）
		interval := integralInterval(gap, gapHistory)

		pc.mu.Lock()
		h := append(gapHistory, gap)
		if len(h) > gapHistoryLen {
			h = h[len(h)-gapHistoryLen:]
		}
		pc.gaps[mac] = h
		pc.mu.Unlock()

		pc.store.Insert(PowerSample{
			MAC:       mac,
			TS:        now,
			PowerW:    powerW,
			IntervalS: interval,
		})
	}
}

// detectPlugFlips 检测 plug_N.on 状态翻转并入库。
// 首次观察只建立基线不记录，避免进程启动时把当前状态当作翻转。
func (pc *PowerCollector) detectPlugFlips(mac string, payload map[string]any) {
	now := time.Now().Unix()
	type plugFlip struct {
		plug, prev, cur int
	}
	var flips []plugFlip

	pc.mu.Lock()
	state := pc.plugOn[mac]
	for i := 0; i < 6; i++ {
		raw, ok := payload[fmt.Sprintf("plug_%d", i)]
		if !ok {
			continue
		}
		obj, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		onVal, ok := obj["on"].(float64)
		if !ok {
			continue
		}
		cur := int(onVal)
		if state == nil {
			state = make(map[int]int)
			pc.plugOn[mac] = state
		}
		prev, seen := state[i]
		state[i] = cur
		if !seen || prev == cur {
			continue
		}
		flips = append(flips, plugFlip{plug: i, prev: prev, cur: cur})
	}
	pc.mu.Unlock()

	for _, f := range flips {
		rec := TaskLogRecord{
			MAC:     mac,
			PlugIdx: f.plug,
			PrevOn:  f.prev,
			CurOn:   f.cur,
			TS:      now,
		}
		pc.store.InsertTaskLog(rec)
		// 翻转即推 webhook（异步；仅出站 HTTP，不向设备发 MQTT）
		notifyWebhookAsync(rec)
	}
}

func (pc *PowerCollector) Stop() {
	if pc.client != nil {
		pc.client.Disconnect(1000)
	}
	close(pc.done)
}