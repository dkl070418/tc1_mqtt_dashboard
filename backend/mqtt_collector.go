package main

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type PowerCollector struct {
	store  *PowerStore
	client mqtt.Client
	done   chan struct{}
}

func NewPowerCollector(store *PowerStore) *PowerCollector {
	return &PowerCollector{
		store: store,
		done:  make(chan struct{}),
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
	for _, mac := range macs {
		topics := []string{
			fmt.Sprintf("device/ztc1/%s/sensor", mac),
			fmt.Sprintf("device/ztc1/%s/state", mac),
		}
		for _, topic := range topics {
			t := c.Subscribe(topic, 0, pc.makeHandler(mac))
			t.WaitTimeout(5 * time.Second)
			if t.Error() != nil {
				log.Printf("订阅 %s 失败: %v", topic, t.Error())
			} else {
				log.Printf("已订阅 %s", topic)
			}
		}
	}
}

func (pc *PowerCollector) makeHandler(mac string) mqtt.MessageHandler {
	return func(c mqtt.Client, msg mqtt.Message) {
		var payload map[string]any
		if err := json.Unmarshal(msg.Payload(), &payload); err != nil {
			return
		}
		var powerW float64
		if p, ok := payload["power"]; ok {
			switch v := p.(type) {
			case float64:
				powerW = v
			case string:
				fmt.Sscanf(v, "%f", &powerW)
			}
		}
		if powerW == 0 {
			return
		}
		pc.store.Insert(PowerSample{
			MAC:    mac,
			TS:     time.Now().Unix(),
			PowerW: powerW,
		})
	}
}

func (pc *PowerCollector) Stop() {
	if pc.client != nil {
		pc.client.Disconnect(1000)
	}
	close(pc.done)
}