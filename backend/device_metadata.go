package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

type DeviceMetadata struct {
	DisplayName string `json:"display_name,omitempty"`
}

type AdminDevice struct {
	MAC         string `json:"mac"`
	DisplayName string `json:"display_name,omitempty"`
}

type deviceMetadataStore map[string]map[string]DeviceMetadata

type UpdateDeviceMetadataReq struct {
	DisplayName string `json:"display_name"`
}

var (
	deviceMetadataMu sync.RWMutex
	deviceMACPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)
)

func normalizeDeviceMAC(raw string) string {
	return strings.ToLower(strings.NewReplacer(":", "", "-", "").Replace(strings.TrimSpace(raw)))
}

func readDeviceMetadataStore() (deviceMetadataStore, error) {
	store := make(deviceMetadataStore)
	data, err := os.ReadFile(cfg.DeviceMetadataPath)
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
		return nil, fmt.Errorf("解析设备元数据失败: %w", err)
	}
	return store, nil
}

func writeDeviceMetadataStore(store deviceMetadataStore) error {
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(cfg.DeviceMetadataPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".device-metadata-*.tmp")
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
	return os.Rename(tmpPath, cfg.DeviceMetadataPath)
}

func getAdminDevicesWithMetadata(macs []string) ([]AdminDevice, error) {
	deviceMetadataMu.RLock()
	defer deviceMetadataMu.RUnlock()
	store, err := readDeviceMetadataStore()
	if err != nil {
		return nil, err
	}
	userMetadata := store[cfg.AdminUsername]
	devices := make([]AdminDevice, 0, len(macs))
	for _, rawMAC := range macs {
		mac := normalizeDeviceMAC(rawMAC)
		device := AdminDevice{MAC: mac}
		if metadata, ok := userMetadata[mac]; ok {
			device.DisplayName = metadata.DisplayName
		}
		devices = append(devices, device)
	}
	return devices, nil
}

func handleAdminDeviceMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		jsonError(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	mac := normalizeDeviceMAC(strings.TrimPrefix(r.URL.Path, "/api/admin/device-metadata/"))
	if !deviceMACPattern.MatchString(mac) {
		jsonError(w, "设备 MAC 格式错误", http.StatusBadRequest)
		return
	}
	allowedMACs, err := getUserDeviceMACs(cfg.AdminUsername)
	if err != nil {
		jsonError(w, "读取管理员关联设备失败", http.StatusInternalServerError)
		return
	}
	allowed := false
	for _, candidate := range allowedMACs {
		if normalizeDeviceMAC(candidate) == mac {
			allowed = true
			break
		}
	}
	if !allowed {
		jsonError(w, "无权修改该设备", http.StatusForbidden)
		return
	}
	var req UpdateDeviceMetadataReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if len([]rune(req.DisplayName)) > 64 {
		jsonError(w, "设备名称不能超过 64 个字符", http.StatusBadRequest)
		return
	}
	deviceMetadataMu.Lock()
	defer deviceMetadataMu.Unlock()
	store, err := readDeviceMetadataStore()
	if err != nil {
		jsonError(w, "读取设备元数据失败", http.StatusInternalServerError)
		return
	}
	username := cfg.AdminUsername
	if store[username] == nil {
		store[username] = make(map[string]DeviceMetadata)
	}
	if req.DisplayName == "" {
		delete(store[username], mac)
		if len(store[username]) == 0 {
			delete(store, username)
		}
	} else {
		store[username][mac] = DeviceMetadata{DisplayName: req.DisplayName}
	}
	if err := writeDeviceMetadataStore(store); err != nil {
		jsonError(w, "保存设备元数据失败", http.StatusInternalServerError)
		return
	}
	jsonResp(w, AdminDevice{MAC: mac, DisplayName: req.DisplayName})
}
