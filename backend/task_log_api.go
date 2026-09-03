package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// 设备时区：任务 hour/minute 由固件按设备本地时间执行，
// 与功耗汇总保持一致使用东八区做推断。
var taskLogZone = time.FixedZone("CST", 8*3600)

const (
	taskTypeTask   = "task"   // 状态翻转与某任务预期时刻匹配 → 定时触发
	taskTypeManual = "manual" // 无任务匹配 → 手动操作
	taskTypeMissed = "missed" // 周期任务预期时刻无状态翻转 → 未观察到执行
)

// taskConfig 任务配置的规范化结构
type taskConfig struct {
	idx    int
	hour   int
	minute int
	repeat int // 0=一次, bit0=周一...bit6=周日, 127=每天
	action int
	on     int
}

func anyToInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return i, true
		}
	}
	return 0, false
}

// loadPlugTasks 从设备配置解析某插口的启用任务列表
func loadPlugTasks(tasks map[string]map[string]any, plugIdx int) []taskConfig {
	var out []taskConfig
	plugTasks, ok := tasks[strconv.Itoa(plugIdx)]
	if !ok {
		return out
	}
	for key, raw := range plugTasks {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		on, ok := anyToInt(m["on"])
		if !ok || on != 1 {
			continue
		}
		hour, ok1 := anyToInt(m["hour"])
		minute, ok2 := anyToInt(m["minute"])
		repeat, _ := anyToInt(m["repeat"])
		action, ok3 := anyToInt(m["action"])
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		idx := -1
		fmt.Sscanf(key, "task_%d", &idx)
		out = append(out, taskConfig{
			idx: idx, hour: hour, minute: minute,
			repeat: repeat, action: action, on: on,
		})
	}
	return out
}

// matchTaskAtFlip 判断状态翻转是否与某个启用任务的预期时刻匹配（±2 分钟）。
// 候选时刻 = 翻转当天的 hour:minute（设备时区）；repeat 位掩码按翻转星期校验；
// repeat=0（一次）不做星期校验（无法得知其创建日期）。
func matchTaskAtFlip(tasks map[string]map[string]any, plugIdx int, flipTS int64, curOn int) (taskConfig, int64, bool) {
	flipT := time.Unix(flipTS, 0).In(taskLogZone)
	candidates := loadPlugTasks(tasks, plugIdx)
	for _, t := range candidates {
		if t.action != curOn {
			continue
		}
		cand := time.Date(flipT.Year(), flipT.Month(), flipT.Day(), t.hour, t.minute, 0, 0, taskLogZone)
		diff := flipTS - cand.Unix()
		if diff < 0 {
			diff = -diff
		}
		if diff > 120 {
			continue
		}
		if t.repeat != 0 {
			// 周一=bit0 ... 周日=bit6
			bit := (int(flipT.Weekday()) + 6) % 7
			if t.repeat&(1<<bit) == 0 {
				continue
			}
		}
		return t, cand.Unix(), true
	}
	return taskConfig{}, 0, false
}

// collectExpectedOccurrences 生成时间窗内周期任务（repeat != 0）的预期触发点。
// 一次性任务（repeat = 0）触发后固件行为未知（可能不自动停用），为避免
// 误报"未执行"，不参与该项检测。
func collectExpectedOccurrences(tasks map[string]map[string]any, plugIdx int, from, to int64) []map[string]any {
	var out []map[string]any
	for _, t := range loadPlugTasks(tasks, plugIdx) {
		if t.repeat == 0 {
			continue
		}
		start := time.Unix(from, 0).In(taskLogZone)
		// 从窗口起点当天 00:00 开始逐日生成候选
		dayStart := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, taskLogZone)
		for d := dayStart; d.Unix() <= to; d = d.AddDate(0, 0, 1) {
			bit := (int(d.Weekday()) + 6) % 7
			if t.repeat&(1<<bit) == 0 {
				continue
			}
			cand := time.Date(d.Year(), d.Month(), d.Day(), t.hour, t.minute, 0, 0, taskLogZone)
			if cand.Unix() < from || cand.Unix() > to {
				continue
			}
			out = append(out, map[string]any{
				"plug_idx":    plugIdx,
				"task_idx":    t.idx,
				"action":      t.action,
				"expected_ts": cand.Unix(),
			})
		}
	}
	return out
}

// handleTaskLogs GET /api/admin/task-logs?mac=&plug=&limit=&days=
// 返回插口状态变化记录，并结合任务配置推断执行类型：
//   - task:   定时触发（推断）
//   - manual: 手动操作（推断）
//   - missed: 周期任务在预期时刻 ±2 分钟内未观察到状态变化
func handleTaskLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	if powerStore == nil {
		jsonError(w, "功耗采集未启用，任务日志不可用", http.StatusServiceUnavailable)
		return
	}

	q := r.URL.Query()
	mac := strings.ToLower(strings.TrimSpace(q.Get("mac")))
	if !deviceConfigPattern.MatchString(mac) {
		jsonError(w, "MAC 格式错误", http.StatusBadRequest)
		return
	}

	// 校验设备归属管理员账号
	allowedMACs, err := getUserDeviceMACs(cfg.AdminUsername)
	if err != nil {
		jsonError(w, "读取设备列表失败", http.StatusInternalServerError)
		return
	}
	allowed := false
	for _, m := range allowedMACs {
		if normalizeDeviceMAC(m) == mac {
			allowed = true
			break
		}
	}
	if !allowed {
		jsonError(w, "无权查看该设备", http.StatusForbidden)
		return
	}

	limit := 500
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= 2000 {
		limit = v
	}
	days := 1
	if v, err := strconv.Atoi(q.Get("days")); err == nil && v > 0 && v <= 15 {
		days = v
	}

	var plugFilter *int
	if ps := strings.TrimSpace(q.Get("plug")); ps != "" {
		if p, err := strconv.Atoi(ps); err == nil && p >= 0 && p < 6 {
			plugFilter = &p
		} else {
			jsonError(w, "plug 参数错误", http.StatusBadRequest)
			return
		}
	}

	// 查询状态翻转记录（时间倒序）
	records, err := powerStore.QueryTaskLogs(mac, plugFilter, limit)
	if err != nil {
		jsonError(w, "查询任务日志失败", http.StatusInternalServerError)
		return
	}

	// 读取任务配置做关联推断
	configs, err := getAdminDeviceConfig()
	if err != nil {
		jsonError(w, "读取设备配置失败", http.StatusInternalServerError)
		return
	}
	tasks := configs[mac].Tasks

	now := time.Now().Unix()
	from := now - int64(days)*86400

	type entry map[string]any
	entries := make([]entry, 0, len(records))

	// 1) 翻转记录 → task / manual
	for _, rec := range records {
		e := entry{
			"ts":      rec.TS,
			"plug_idx": rec.PlugIdx,
			"prev_on": rec.PrevOn,
			"cur_on":  rec.CurOn,
		}
		if rec.TS >= from {
			if t, expectedTS, ok := matchTaskAtFlip(tasks, rec.PlugIdx, rec.TS, rec.CurOn); ok {
				e["type"] = taskTypeTask
				e["task_idx"] = t.idx
				e["expected_ts"] = expectedTS
				entries = append(entries, e)
				continue
			}
		}
		e["type"] = taskTypeManual
		entries = append(entries, e)
	}

	// 2) 周期任务预期触发点 → missed（与翻转匹配的跳过）
	plugRange := func() []int {
		if plugFilter != nil {
			return []int{*plugFilter}
		}
		return []int{0, 1, 2, 3, 4, 5}
	}
	for _, p := range plugRange() {
		for _, occ := range collectExpectedOccurrences(tasks, p, from, now) {
			hit := false
			for _, rec := range records {
				if rec.PlugIdx != p || rec.TS < from {
					continue
				}
				diff := rec.TS - occ["expected_ts"].(int64)
				if diff < 0 {
					diff = -diff
				}
				if diff <= 120 && rec.CurOn == occ["action"].(int) {
					hit = true
					break
				}
			}
			if !hit {
				entries = append(entries, entry{
					"type":        taskTypeMissed,
					"plug_idx":    occ["plug_idx"],
					"task_idx":    occ["task_idx"],
					"action":      occ["action"],
					"expected_ts": occ["expected_ts"],
					"ts":          occ["expected_ts"],
				})
			}
		}
	}

	// 按时间倒序
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			ti, _ := entries[i]["ts"].(int64)
			tj, _ := entries[j]["ts"].(int64)
			if tj > ti {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}

	jsonResp(w, map[string]any{
		"logs":        entries,
		"window_days": days,
		"generated_at": now,
	})
}
