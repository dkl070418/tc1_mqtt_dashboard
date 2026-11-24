package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestV1TimeSyncPayload 校时 payload 必须与前端 emit('send-cmd', {mac, setting:{time}})
// 经 JSON.stringify 出来的形状一致, 否则 v1 固件解析不到 setting.time。
func TestV1TimeSyncPayload(t *testing.T) {
	got := v1TimeSyncPayload("a1b2c3d4e5f6", 1795000000)
	want := `{"mac":"a1b2c3d4e5f6","setting":{"time":1795000000}}`
	if got != want {
		t.Fatalf("payload=%s\n期望=%s", got, want)
	}
	// 必须能被解析回 setting.time
	var msg map[string]any
	if err := json.Unmarshal([]byte(got), &msg); err != nil {
		t.Fatalf("payload 不是合法 JSON: %v", err)
	}
	setting, ok := msg["setting"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 setting 对象: %v", msg)
	}
	if _, ok := setting["time"].(float64); !ok {
		t.Fatalf("缺少 setting.time: %v", setting)
	}
}

func TestTimeSyncDue(t *testing.T) {
	base := time.Date(2026, time.November, 23, 12, 0, 0, 0, cstZone)
	sixHours := 6 * time.Hour
	cases := []struct {
		name     string
		last     time.Time
		now      time.Time
		interval time.Duration
		want     bool
	}{
		{name: "刚启动(零值)立即校一次", last: time.Time{}, now: base, interval: sixHours, want: true},
		{name: "差6小时整到点", last: base, now: base.Add(sixHours), interval: sixHours, want: true},
		{name: "差5小时59分59秒未到点", last: base, now: base.Add(sixHours - time.Second), interval: sixHours, want: false},
		{name: "差7小时已过点", last: base, now: base.Add(7 * time.Hour), interval: sixHours, want: true},
		{name: "系统时间回拨不校", last: base, now: base.Add(-time.Hour), interval: sixHours, want: false},
		{name: "关闭(interval=0)永不校", last: time.Time{}, now: base, interval: 0, want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := timeSyncDue(c.last, c.now, c.interval); got != c.want {
				t.Fatalf("timeSyncDue=%v, 期望 %v", got, c.want)
			}
		})
	}
}

func TestV1TimeSyncIntervalHours(t *testing.T) {
	old := cfg.V1TimeSyncIntervalHours
	defer func() { cfg.V1TimeSyncIntervalHours = old }()

	cfg.V1TimeSyncIntervalHours = nil
	if got := v1TimeSyncIntervalHours(); got != 6 {
		t.Fatalf("未配置应为默认 6, 实得 %d", got)
	}
	for _, c := range []struct{ in, want int }{
		{0, 0}, {-1, 0}, {1, 1}, {6, 6}, {24, 24}, {168, 168}, {9999, 168},
	} {
		v := c.in
		cfg.V1TimeSyncIntervalHours = &v
		if got := v1TimeSyncIntervalHours(); got != c.want {
			t.Fatalf("配置 %d 应为 %d, 实得 %d", c.in, c.want, got)
		}
	}
}

// TestLoadV1Devices 只挑 firmware!=v2 的设备; 未标注 firmware 的按 v1;
// MAC 需要归一化去重; 非法键跳过。
func TestLoadV1Devices(t *testing.T) {
	oldCfg := cfg
	defer func() { cfg = oldCfg }()

	dir := t.TempDir()
	path := filepath.Join(dir, "device_config.json")
	fixture := `{
	  "admin": {
	    "a1b2c3d4e5f6": {"firmware": "v1"},
	    "A1B2C3D4E5F6": {},
	    "001122334455": {"firmware": "v1"},
	    "AA:BB:CC:DD:EE:FF": {"firmware": "v2"},
	    "not-a-mac": {"firmware": "v1"}
	  },
	  "someoneelse": {
	    "999999999999": {"firmware": "v1"}
	  }
	}`
	if err := os.WriteFile(path, []byte(fixture), 0644); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	cfg.AdminUsername = "admin"
	cfg.DeviceConfigPath = path

	got, err := loadV1Devices()
	if err != nil {
		t.Fatalf("loadV1Devices 失败: %v", err)
	}
	want := []string{"001122334455", "a1b2c3d4e5f6"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("目标设备=%v, 期望 %v (v2 必须被排除, 大小写重复必须合并, 非法键必须跳过)", got, want)
	}
}

// TestMissingTimeSyncACL 管理员 ACL 里没有的设备必须被点出来 —— mosquitto 丢包是静默的。
func TestMissingTimeSyncACL(t *testing.T) {
	oldCfg := cfg
	defer func() { cfg = oldCfg }()

	dir := t.TempDir()
	aclPath := filepath.Join(dir, "acl")
	acl := strings.Join([]string{
		"user admin",
		"topic readwrite device/ztc1/001122334455/#",
		"topic readwrite device/ztc1/set",
		"",
		"user someoneelse",
		"topic readwrite device/ztc1/a1b2c3d4e5f6/#",
		"",
	}, "\n")
	if err := os.WriteFile(aclPath, []byte(acl), 0644); err != nil {
		t.Fatalf("写测试 ACL 失败: %v", err)
	}
	cfg.AdminUsername = "admin"
	cfg.ACLPath = aclPath

	got := missingTimeSyncACL([]string{"001122334455", "a1b2c3d4e5f6"})
	if !reflect.DeepEqual(got, []string{"a1b2c3d4e5f6"}) {
		t.Fatalf("缺 ACL=%v, 期望 [a1b2c3d4e5f6]", got)
	}
	if got := missingTimeSyncACL(nil); got != nil {
		t.Fatalf("无目标时应为 nil, 实得 %v", got)
	}
}

// TestTimeSyncDiagnosticsEndpoint 排查接口: 调度器没起来要明确报 503, 起来了要给出目标与间隔。
func TestTimeSyncDiagnosticsEndpoint(t *testing.T) {
	oldCfg, oldSched := cfg, taskScheduler
	defer func() { cfg, taskScheduler = oldCfg, oldSched }()

	dir := t.TempDir()
	path := filepath.Join(dir, "device_config.json")
	if err := os.WriteFile(path, []byte(`{"admin":{"a1b2c3d4e5f6":{"firmware":"v1"}}}`), 0644); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	cfg.AdminUsername = "admin"
	cfg.DeviceConfigPath = path

	taskScheduler = nil
	rec := httptest.NewRecorder()
	handleTimeSyncDiagnostics(rec, httptest.NewRequest(http.MethodGet, "/api/admin/diagnostics/time-sync", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("调度器未启动应为 503, 实得 %d", rec.Code)
	}

	taskScheduler = NewTaskScheduler(nil)
	rec = httptest.NewRecorder()
	handleTimeSyncDiagnostics(rec, httptest.NewRequest(http.MethodGet, "/api/admin/diagnostics/time-sync", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("应为 200, 实得 %d", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("返回不是合法 JSON: %v", err)
	}
	if out["enabled"] != true {
		t.Fatalf("默认应启用: %v", out["enabled"])
	}
	if out["interval_hours"] != float64(6) {
		t.Fatalf("默认间隔应为 6 小时: %v", out["interval_hours"])
	}
	targets, _ := out["current_targets"].([]any)
	if len(targets) != 1 || targets[0] != "a1b2c3d4e5f6" {
		t.Fatalf("目标设备不对: %v", out["current_targets"])
	}
	if _, hasLast := out["last_sync"]; hasLast {
		t.Fatalf("还没校过时不应出现 last_sync: %v", out)
	}
}
