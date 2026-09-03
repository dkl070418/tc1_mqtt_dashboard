package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

var powerStore *PowerStore

func handlePowerQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	mac := strings.ToLower(r.URL.Query().Get("mac"))
	if mac == "" || len(mac) != 12 {
		jsonError(w, "缺少有效的 mac 参数", http.StatusBadRequest)
		return
	}
	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	granularity := r.URL.Query().Get("granularity")
	if granularity == "" {
		granularity = "1min"
	}
	now := time.Now()
	from := now.Add(-24 * time.Hour).Unix()
	to := now.Unix()
	if fromStr != "" {
		if v, err := strconv.ParseInt(fromStr, 10, 64); err == nil {
			from = v
		}
	}
	if toStr != "" {
		if v, err := strconv.ParseInt(toStr, 10, 64); err == nil {
			to = v
		}
	}
	if powerStore == nil {
		jsonResp(w, []any{})
		return
	}
	data, err := powerStore.QueryPower(mac, from, to, granularity)
	if err != nil {
		jsonError(w, "查询失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if data == nil {
		data = []map[string]any{}
	}
	jsonResp(w, data)
}

func handlePowerSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	mac := strings.ToLower(r.URL.Query().Get("mac"))
	if mac == "" || len(mac) != 12 {
		jsonError(w, "缺少有效的 mac 参数", http.StatusBadRequest)
		return
	}
	if powerStore == nil {
		jsonResp(w, map[string]any{
			"mac": mac, "current_power_w": 0,
			"today_energy_kwh": 0, "month_energy_kwh": 0,
			"enabled": false, // 功耗采集未启用（power_collect_enabled）
		})
		return
	}
	summary, err := powerStore.GetSummary(mac)
	if err != nil {
		jsonError(w, "查询失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	summary["enabled"] = true
	jsonResp(w, summary)
}