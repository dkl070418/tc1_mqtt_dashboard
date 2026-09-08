package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// DeviceConfig 保存单台设备在 Web 端编辑的"配置层"数据：
//   - Remarks: 插口备注/名称（v1 与 v2 的本地命名）
//   - Layout:  插口布局（auto/1x6/2x3/3x2）
//   - Tasks:   各插口定时任务配置（期望配置，用于跨端回显）
//
// 与设备实时上报的状态分离，避免被设备 state 覆盖。
type DeviceConfig struct {
	Layout  string                    `json:"layout,omitempty"`
	Remarks map[string]string         `json:"remarks,omitempty"`
	Tasks   map[string]map[string]any `json:"tasks,omitempty"`
	// Firmware: 设备固件版本 "v1" | "v2"，决定 topic 订阅方案，跨端持久化
	Firmware string `json:"firmware,omitempty"`
	Version  string `json:"version,omitempty"`
}

type deviceConfigStore map[string]map[string]DeviceConfig

type UpdateDeviceConfigReq struct {
	Layout  string                    `json:"layout,omitempty"`
	Remarks map[string]string         `json:"remarks,omitempty"`
	Tasks   map[string]map[string]any `json:"tasks,omitempty"`
	// TasksReplace=true 时 tasks 按全量替换保存（前端提交完整任务表，
	// 未包含的任务视为删除）；默认 false 保持旧版"按插口/任务键合并"语义
	TasksReplace bool   `json:"tasks_replace,omitempty"`
	Firmware     string `json:"firmware,omitempty"`
	Version      string `json:"version,omitempty"`
}

var (
	deviceConfigMu      sync.RWMutex
	deviceConfigPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)
)

// putAdminDeviceConfig 的业务错误，handler 据此映射 HTTP 状态码
var (
	errDeviceMACFormat = errors.New("设备 MAC 格式错误")
	errDeviceForbidden = errors.New("无权修改该设备")
	errDeviceFirmware  = errors.New("固件版本只能是 v1 或 v2")
)

func readDeviceConfigStore() (deviceConfigStore, error) {
	store := make(deviceConfigStore)
	data, err := os.ReadFile(cfg.DeviceConfigPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return store, nil
	}
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("解析设备配置失败: %w", err)
	}
	return store, nil
}

func writeDeviceConfigStore(store deviceConfigStore) error {
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(cfg.DeviceConfigPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".device-config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, cfg.DeviceConfigPath)
}

// getAdminDeviceConfig 返回当前管理员账号下全部设备的配置
func getAdminDeviceConfig() (map[string]DeviceConfig, error) {
	deviceConfigMu.RLock()
	defer deviceConfigMu.RUnlock()
	store, err := readDeviceConfigStore()
	if err != nil {
		return nil, err
	}
	if store[cfg.AdminUsername] == nil {
		return map[string]DeviceConfig{}, nil
	}
	return store[cfg.AdminUsername], nil
}

// sanitizeTaskValues 任务值类型清洗: 前端(旧版)可能以字符串保存 hour/minute 等,
// 原版 v1 固件对非数字不健壮; v2 调度器/固件也需要数字。写入前统一归一。
func sanitizeTaskValues(tasks map[string]map[string]any) map[string]map[string]any {
	if tasks == nil {
		return nil
	}
	for plugIdx, tasksMap := range tasks {
		for taskKey, raw := range tasksMap {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			for _, k := range []string{"hour", "minute", "repeat", "action", "on"} {
				if v, exists := m[k]; exists {
					switch n := v.(type) {
					case float64:
						// JSON 数字已是 float64, 归一为整数值
						m[k] = float64(int64(n))
					case string:
						if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
							m[k] = f
						}
					}
				}
			}
			tasksMap[taskKey] = m
		}
		tasks[plugIdx] = tasksMap
	}
	return tasks
}

// putAdminDeviceConfig 保存当前管理员账号下某台设备的配置
func putAdminDeviceConfig(mac string, cfg_in UpdateDeviceConfigReq) error {
	mac = normalizeDeviceMAC(mac)
	if !deviceConfigPattern.MatchString(mac) {
		return errDeviceMACFormat
	}

	allowedMACs, err := getUserDeviceMACs(cfg.AdminUsername)
	if err != nil {
		return err
	}
	allowed := false
	for _, candidate := range allowedMACs {
		if normalizeDeviceMAC(candidate) == mac {
			allowed = true
			break
		}
	}
	if !allowed {
		return errDeviceForbidden
	}

	deviceConfigMu.Lock()
	defer deviceConfigMu.Unlock()
	store, err := readDeviceConfigStore()
	if err != nil {
		return err
	}
	username := cfg.AdminUsername
	if store[username] == nil {
		store[username] = make(map[string]DeviceConfig)
	}
	existing := store[username][mac]

	// 先深拷贝现有配置作为基底，避免本次请求未提交的字段被丢
	next := DeviceConfig{
		Layout:   existing.Layout,
		Remarks:  deepCopyStringMap(existing.Remarks),
		Tasks:    deepCopyTasks(existing.Tasks),
		Firmware: existing.Firmware,
		Version:  existing.Version,
	}
	if cfg_in.Version != "" {
		next.Version = cfg_in.Version
	}
	if cfg_in.Layout != "" {
		next.Layout = cfg_in.Layout
	}
	if cfg_in.Firmware != "" {
		if cfg_in.Firmware != "v1" && cfg_in.Firmware != "v2" {
			return errDeviceFirmware
		}
		next.Firmware = cfg_in.Firmware
	}
	if cfg_in.Remarks != nil {
		// 插口备注每次全量提交，直接替换
		next.Remarks = deepCopyStringMap(cfg_in.Remarks)
	}
	if cfg_in.Tasks != nil {
		if cfg_in.TasksReplace {
			// 全量替换：前端提交完整任务表，未包含者视为删除（解决"任务无法删除"）
			next.Tasks = sanitizeTaskValues(deepCopyTasks(cfg_in.Tasks))
		} else {
			// 兼容旧版：按任务键合并，避免前端某次只提交不完整 tasks 时覆盖/丢失其他任务
			if next.Tasks == nil {
				next.Tasks = make(map[string]map[string]any)
			}
			for plugIdx, tasksMap := range cfg_in.Tasks {
				if next.Tasks[plugIdx] == nil {
					next.Tasks[plugIdx] = make(map[string]any)
				}
				for taskKey, taskData := range tasksMap {
					next.Tasks[plugIdx][taskKey] = taskData
				}
			}
			next.Tasks = sanitizeTaskValues(next.Tasks)
		}
	}

	store[username][mac] = next
	return writeDeviceConfigStore(store)
}

func handleAdminGetDeviceConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	configs, err := getAdminDeviceConfig()
	if err != nil {
		jsonError(w, "读取设备配置失败", http.StatusInternalServerError)
		return
	}
	jsonResp(w, configs)
}

func handleAdminPutDeviceConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		jsonError(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	mac := normalizeDeviceMAC(strings.TrimPrefix(r.URL.Path, "/api/admin/device-config/"))
	var req UpdateDeviceConfigReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if err := putAdminDeviceConfig(mac, req); err != nil {
		switch {
		case errors.Is(err, errDeviceForbidden):
			jsonError(w, err.Error(), http.StatusForbidden)
		case errors.Is(err, errDeviceMACFormat):
			jsonError(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, errDeviceFirmware):
			jsonError(w, err.Error(), http.StatusBadRequest)
		default:
			jsonError(w, "保存设备配置失败", http.StatusInternalServerError)
		}
		return
	}
	jsonResp(w, map[string]string{"status": "ok"})
}

func deepCopyStringMap(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func deepCopyTasks(src map[string]map[string]any) map[string]map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]map[string]any, len(src))
	for plugIdx, tasksMap := range src {
		m := make(map[string]any, len(tasksMap))
		for k, v := range tasksMap {
			m[k] = v
		}
		dst[plugIdx] = m
	}
	return dst
}
