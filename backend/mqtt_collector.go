package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type PowerCollector struct {
	store   *PowerStore
	client  mqtt.Client
	done    chan struct{}
	mu      sync.Mutex
	lastTS  map[string]int64 // mac -> 上次采样时间，用于计算积分间隔
	plugOn  map[string]map[int]int // mac -> (插口索引 -> 上次 on 状态)，用于检测状态翻转
}

func NewPowerCollector(store *PowerStore) *PowerCollector {
	return &PowerCollector{
		store:  store,
		done:   make(chan struct{}),
		lastTS: make(map[string]int64),
		plugOn: make(map[string]map[int]int),
	}
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
		} {
			t := c.Subscribe(topic, 0, haHandler)
			t.WaitTimeout(5 * time.Second)
			if t.Error() != nil {
				log.Printf("订阅 %s 失败: %v", topic, t.Error())
			} else {
				log.Printf("已订阅 %s", topic)
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

		var powerW float64
		if p, ok := payload["power"]; ok {
			switch v := p.(type) {
			case float64:
				powerW = v
			case string:
				fmt.Sscanf(v, "%f", &powerW)
			}
		}
		if powerW < 0 {
			// 负值功率：疑似设备功耗芯片损坏，不采集统计
			return
		}
		now := time.Now().Unix()
		pc.mu.Lock()
		last, had := pc.lastTS[mac]
		pc.lastTS[mac] = now
		pc.mu.Unlock()

		interval := 2.0 // 默认 2 秒（首次采样或重置用）
		if had && now > last {
			interval = float64(now - last)
		}
		if interval < 0.5 {
			interval = 0.5
		}
		if interval > 30 {
			interval = 30 // 防止断线重连后产生巨大间隔
		}

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