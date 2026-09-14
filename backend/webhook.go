package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 通用 Webhook：检测到插口状态翻转时推送。
// 与任务日志(task_logs)同源，覆盖 V1/V2 与手动/定时；不向排插下发任何 MQTT。
//
// 防刷屏：webhook_throttle_seconds > 0 时，窗口内的多次翻转合并为一条
// （event=plug_flip_batch），避免总开关/连点时机器人被刷爆。

var webhookHTTPClient = &http.Client{Timeout: 8 * time.Second}

// 单条默认 body；批量时 ${event}=${action} 等仍可解析（首条事件 + count/flips）
const defaultWebhookBody = `{"event":"${event}","mac":"${mac}","device":"${device}","plug":${plug},"plug_name":"${plug_name}","prev":${prev},"cur":${cur},"action":"${action}","time":"${time}","count":${count},"message":"${message}","flips":"${flips}"}`

// FlipEvent 插口翻转事件（webhook 模板变量来源）
type FlipEvent struct {
	MAC  string
	Plug int
	Prev int
	Cur  int
	TS   int64
}

func actionOf(cur int) string {
	if cur == 1 {
		return "on"
	}
	return "off"
}

func actionCN(a string) string {
	if a == "on" {
		return "开启"
	}
	return "关闭"
}

// flipNameCache 缓存设备/插口名：优先配置备注，其次 MQTT 上报的 name/setting.name
type flipNameCache struct {
	mu      sync.RWMutex
	devices map[string]string            // mac -> 显示名
	plugs   map[string]map[int]string    // mac -> plugIdx -> 插口名
}

var flipNames = flipNameCache{
	devices: make(map[string]string),
	plugs:   make(map[string]map[int]string),
}

// observeFlipNames 从设备 state/sensor payload 提取名称（V2 有 setting.name）
func observeFlipNames(mac string, payload map[string]any) {
	mac = strings.ToLower(mac)
	flipNames.mu.Lock()
	defer flipNames.mu.Unlock()
	if n, ok := payload["name"].(string); ok && strings.TrimSpace(n) != "" {
		flipNames.devices[mac] = strings.TrimSpace(n)
	}
	if flipNames.plugs[mac] == nil {
		flipNames.plugs[mac] = make(map[int]string)
	}
	for i := 0; i < 6; i++ {
		raw, ok := payload[fmt.Sprintf("plug_%d", i)]
		if !ok {
			continue
		}
		obj, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		setting, ok := obj["setting"].(map[string]any)
		if !ok {
			continue
		}
		if n, ok := setting["name"].(string); ok && strings.TrimSpace(n) != "" {
			flipNames.plugs[mac][i] = strings.TrimSpace(n)
		}
	}
}

// lookupPlugRemark 读 device_config.json 里管理员保存的插口备注
func lookupPlugRemark(mac string, plug int) string {
	mac = normalizeDeviceMAC(mac)
	if !deviceMACPattern.MatchString(mac) {
		return ""
	}
	cfgs, err := getAdminDeviceConfig()
	if err != nil {
		return ""
	}
	remarks := cfgs[mac].Remarks
	if remarks == nil {
		return ""
	}
	return strings.TrimSpace(remarks[strconv.Itoa(plug)])
}

// deviceLabel 设备名：配置显示名 > MQTT 缓存 > MAC
func deviceLabel(mac string) string {
	if n := lookupDeviceDisplayName(mac); n != "" {
		return n
	}
	flipNames.mu.RLock()
	n := flipNames.devices[strings.ToLower(mac)]
	flipNames.mu.RUnlock()
	if n != "" {
		return n
	}
	return mac
}

// plugLabel 插口名：配置备注 > MQTT setting.name > 插口N（N 与前端一致，从 1 起）
func plugLabel(mac string, plug int) string {
	if n := lookupPlugRemark(mac, plug); n != "" {
		return n
	}
	flipNames.mu.RLock()
	n := flipNames.plugs[strings.ToLower(mac)][plug]
	flipNames.mu.RUnlock()
	if n != "" {
		return n
	}
	return fmt.Sprintf("插口%d", plug+1)
}

// plugLabelShort 显示用：有名字就带 1 基序号「插口1(MACmini)」
func plugLabelShort(mac string, plug int) string {
	name := plugLabel(mac, plug)
	if name == fmt.Sprintf("插口%d", plug+1) {
		return name
	}
	return fmt.Sprintf("插口%d(%s)", plug+1, name)
}

func (e FlipEvent) vars() map[string]string {
	return flipBatchVars([]FlipEvent{e})
}

// flipBatchVars 1 条与 N 条共用模板变量；N>1 时 event=plug_flip_batch
func flipBatchVars(batch []FlipEvent) map[string]string {
	if len(batch) == 0 {
		return map[string]string{}
	}
	first := batch[0]
	zone := time.FixedZone("CST", 8*3600)
	ts := time.Unix(first.TS, 0).In(zone)
	evtName := "plug_flip"
	if len(batch) > 1 {
		evtName = "plug_flip_batch"
	}

	dev := deviceLabel(first.MAC)
	pName := plugLabel(first.MAC, first.Plug)

	var flipsParts []string
	var msgParts []string
	for _, e := range batch {
		a := actionOf(e.Cur)
		d := deviceLabel(e.MAC)
		p := plugLabelShort(e.MAC, e.Plug)
		flipsParts = append(flipsParts, fmt.Sprintf("%s/p%d:%s", e.MAC, e.Plug, a))
		msgParts = append(msgParts, fmt.Sprintf("%s 的 %s %s", d, p, actionCN(a)))
	}
	message := msgParts[0]
	if len(batch) > 1 {
		message = fmt.Sprintf("%d 次插口变化：%s", len(batch), strings.Join(msgParts, "；"))
	}

	return map[string]string{
		"event":     evtName,
		"mac":       first.MAC,
		"mac_upper": strings.ToUpper(first.MAC),
		"device":    dev,
		"plug":      strconv.Itoa(first.Plug),
		"plug_no":   strconv.Itoa(first.Plug + 1),
		"plug_name": pName,
		"prev":      strconv.Itoa(first.Prev),
		"cur":       strconv.Itoa(first.Cur),
		"action":    actionOf(first.Cur),
		"time":      ts.Format("2006-01-02 15:04:05"),
		"count":     strconv.Itoa(len(batch)),
		"message":   message,
		"flips":     strings.Join(flipsParts, ","),
	}
}

// lookupDeviceDisplayName 读取管理员名下设备显示名；失败时返回空
func lookupDeviceDisplayName(mac string) string {
	mac = normalizeDeviceMAC(mac)
	if !deviceMACPattern.MatchString(mac) {
		return ""
	}
	deviceMetadataMu.RLock()
	defer deviceMetadataMu.RUnlock()
	store, err := readDeviceMetadataStore()
	if err != nil {
		return ""
	}
	return store[cfg.AdminUsername][mac].DisplayName
}

func renderTemplate(tpl string, vars map[string]string) string {
	if tpl == "" {
		return ""
	}
	out := tpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "${"+k+"}", v)
	}
	return out
}

// parseHeaderLines 解析多行 Header: "Key: Value"
func parseHeaderLines(raw string) map[string]string {
	headers := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.Index(line, ":")
		if i <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:i])
		v := strings.TrimSpace(line[i+1:])
		if k != "" {
			headers[k] = v
		}
	}
	return headers
}

// sendWebhookOnce 同步发送一次；返回是否成功(HTTP 2xx)
func sendWebhookOnce(url, headerRaw, bodyTpl string, vars map[string]string) error {
	if strings.TrimSpace(url) == "" {
		return fmt.Errorf("webhook_url 为空")
	}
	body := renderTemplate(bodyTpl, vars)
	if bodyTpl == "" {
		body = renderTemplate(defaultWebhookBody, vars)
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimSpace(url), bytes.NewReader([]byte(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range parseHeaderLines(headerRaw) {
		req.Header.Set(k, v)
	}
	resp, err := webhookHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func webhookRetryLimit() int {
	retry := cfg.WebhookRetry
	if retry < 0 {
		retry = 0
	}
	if retry > 5 {
		retry = 5
	}
	return retry
}

// deliverWebhook 异步发送（带有限重试），绝不阻塞 MQTT 采集
func deliverWebhook(batch []FlipEvent) {
	if len(batch) == 0 {
		return
	}
	if !cfg.WebhookEnabled || strings.TrimSpace(cfg.WebhookURL) == "" {
		return
	}
	vars := flipBatchVars(batch)
	retry := webhookRetryLimit()
	go func() {
		var lastErr error
		for attempt := 0; attempt <= retry; attempt++ {
			if attempt > 0 {
				time.Sleep(time.Duration(attempt) * 800 * time.Millisecond)
			}
			lastErr = sendWebhookOnce(cfg.WebhookURL, cfg.WebhookHeaders, cfg.WebhookBody, vars)
			if lastErr == nil {
				if attempt > 0 {
					log.Printf("[webhook] 推送成功(第%d次) count=%d", attempt+1, len(batch))
				}
				return
			}
		}
		log.Printf("[webhook] 推送失败 count=%d err=%v", len(batch), lastErr)
	}()
}

// maxWebhookPending 窗口内最多缓存多少条翻转，超出丢最旧
const maxWebhookPending = 40

type webhookThrottler struct {
	mu      sync.Mutex
	pending []FlipEvent
	timer   *time.Timer
}

var webhookThrottle = &webhookThrottler{}

func (t *webhookThrottler) add(evt FlipEvent) {
	window := time.Duration(webhookThrottleSec()) * time.Second
	if window <= 0 {
		// 关闭节流：立即单条推送
		deliverWebhook([]FlipEvent{evt})
		return
	}
	t.mu.Lock()
	t.pending = append(t.pending, evt)
	if len(t.pending) > maxWebhookPending {
		t.pending = t.pending[len(t.pending)-maxWebhookPending:]
	}
	if t.timer == nil {
		t.timer = time.AfterFunc(window, t.flush)
	}
	t.mu.Unlock()
}

func (t *webhookThrottler) flush() {
	t.mu.Lock()
	batch := t.pending
	t.pending = nil
	t.timer = nil
	t.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	deliverWebhook(batch)
}

// notifyWebhookAsync 翻转入口：有节流窗口则合并，否则立即推
func notifyWebhookAsync(rec TaskLogRecord) {
	if !cfg.WebhookEnabled || strings.TrimSpace(cfg.WebhookURL) == "" {
		return
	}
	webhookThrottle.add(FlipEvent{
		MAC:  rec.MAC,
		Plug: rec.PlugIdx,
		Prev: rec.PrevOn,
		Cur:  rec.CurOn,
		TS:   rec.TS,
	})
}

// handleWebhookTest POST /api/admin/webhook/test — 用示例数据试发（不走节流）
func handleWebhookTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	vars := FlipEvent{
		MAC:  "d0bae4000001",
		Plug: 0,
		Prev: 0,
		Cur:  1,
		TS:   time.Now().Unix(),
	}.vars()
	if err := sendWebhookOnce(cfg.WebhookURL, cfg.WebhookHeaders, cfg.WebhookBody, vars); err != nil {
		jsonError(w, "测试推送失败: "+err.Error(), http.StatusBadGateway)
		return
	}
	jsonResp(w, map[string]string{"message": "测试推送成功"})
}
