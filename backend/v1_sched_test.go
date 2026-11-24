package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestV1SwitchPayload 后端给 v1 下发的开关命令必须与前端同构（原厂固件只认这个形状）
func TestV1SwitchPayload(t *testing.T) {
	got := v1SwitchPayload("a1b2c3d4e5f6", 2, 1)
	want := `{"mac":"a1b2c3d4e5f6","plug_2":{"on":1}}`
	if got != want {
		t.Fatalf("payload=%s\n期望=%s", got, want)
	}
	var msg map[string]any
	if err := json.Unmarshal([]byte(got), &msg); err != nil {
		t.Fatalf("不是合法 JSON: %v", err)
	}
	plug, ok := msg["plug_2"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 plug_2 对象: %v", msg)
	}
	if plug["on"] != float64(1) {
		t.Fatalf("on 字段不对: %v", plug)
	}
}

// TestV1DisableTaskPayload 接管时停用设备侧副本的载荷：必须是"完整任务 + on:0"
func TestV1DisableTaskPayload(t *testing.T) {
	got := v1DisableTaskPayload(taskSpec{
		mac: "a1b2c3d4e5f6", plugIdx: 0, taskKey: "task_3",
		hour: 7, minute: 30, repeat: 127, action: 0,
	})
	want := `{"mac":"a1b2c3d4e5f6","plug_0":{"setting":{"task_3":{"hour":7,"minute":30,"repeat":127,"action":0,"on":0}}}}`
	if got != want {
		t.Fatalf("payload=%s\n期望=%s", got, want)
	}
}

// TestLoadScheduledTasksV1Gate v1 任务只有在"总闸打开 + 任务显式 exec=server"时才纳入托管；
// v2 任务任何时候都由后端执行；缺省/exec=device 的 v1 任务一律不碰（安卓 App 建的任务就是这个形态）。
func TestLoadScheduledTasksV1Gate(t *testing.T) {
	oldCfg := cfg
	defer func() { cfg = oldCfg }()

	dir := t.TempDir()
	path := filepath.Join(dir, "device_config.json")
	fixture := `{
	  "admin": {
	    "a1b2c3d4e5f6": {"tasks": {
	        "0": {"task_0": {"hour": 7, "minute": 0, "repeat": 0, "action": 1, "on": 1, "exec": "server"},
	              "task_1": {"hour": 8, "minute": 0, "repeat": 0, "action": 1, "on": 1, "exec": "device"},
	              "task_2": {"hour": 9, "minute": 0, "repeat": 0, "action": 1, "on": 1}},
	        "3": {"task_1": {"hour": 10, "minute": 0, "repeat": 127, "action": 1, "on": 1, "exec": "server"}}}},
	    "c3d4e5f6a7b8": {"firmware": "v2", "tasks": {"1": {"task_0": {"hour": 11, "minute": 0, "repeat": 31, "action": 1, "on": 1}}}},
	    "b2c3d4e5f6a7": {"firmware": "v1", "tasks": {"5": {"task_0": {"hour": 12, "minute": 0, "repeat": 0, "action": 0, "on": 1, "exec": "server"}}}}
	  }
	}`
	if err := os.WriteFile(path, []byte(fixture), 0644); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	cfg.AdminUsername = "admin"
	cfg.DeviceConfigPath = path

	// 总闸关闭：连 exec=server 的 v1 任务也不接管，只剩 v2
	cfg.V1BackendScheduling = false
	got, err := loadScheduledTasks()
	if err != nil {
		t.Fatalf("loadScheduledTasks 失败: %v", err)
	}
	if len(got) != 1 || got[0].firmware != "v2" {
		t.Fatalf("总闸关闭时应只返回 v2 任务, 实得 %+v", got)
	}

	// 总闸打开：v1 中 exec=server 的 3 条（plug0.task_0 / plug3.task_1 / 另一台 plug5.task_0）+ v2 1 条
	cfg.V1BackendScheduling = true
	got, err = loadScheduledTasks()
	if err != nil {
		t.Fatalf("loadScheduledTasks 失败: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("应返回 4 条任务(v1 exec=server×3 + v2×1), 实得 %d: %+v", len(got), got)
	}
	seen := map[string]bool{}
	for _, tk := range got {
		seen[fmt.Sprintf("%s|%d|%s", tk.mac, tk.plugIdx, tk.taskKey)] = true
	}
	for _, want := range []string{
		"a1b2c3d4e5f6|0|task_0", // exec=server
		"a1b2c3d4e5f6|3|task_1", // exec=server
		"b2c3d4e5f6a7|5|task_0", // exec=server
		"c3d4e5f6a7b8|1|task_0", // v2 恒托管
	} {
		if !seen[want] {
			t.Fatalf("应纳入 %s, 实际: %v", want, seen)
		}
	}
	for _, notWant := range []string{
		"a1b2c3d4e5f6|0|task_1", // exec=device
		"a1b2c3d4e5f6|0|task_2", // 缺省 exec => 设备执行
	} {
		if seen[notWant] {
			t.Fatalf("不应纳入 %s（设备执行）, 实际: %v", notWant, seen)
		}
	}

	// on=0 的任务任何固件都不该进调度
	fixture2 := `{"admin": {"a1b2c3d4e5f6": {"tasks": {"0": {"task_0": {"hour": 7, "minute": 0, "repeat": 0, "action": 1, "on": 0, "exec": "server"}}}}}}`
	if err := os.WriteFile(path, []byte(fixture2), 0644); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}
	if left, _ := loadScheduledTasks(); len(left) != 0 {
		t.Fatalf("停用任务不应进入调度: %+v", left)
	}
}

// TestTaskExec 执行位置的解析：只有显式 "server" 才算服务器执行
func TestTaskExec(t *testing.T) {
	cases := []struct {
		m    map[string]any
		want string
	}{
		{map[string]any{"exec": "server"}, "server"},
		{map[string]any{"exec": "device"}, "device"},
		{map[string]any{}, "device"},
		{map[string]any{"exec": 123}, "device"},
		{map[string]any{"exec": "SERVER"}, "device"}, // 大小写不宽容，避免前端写错时误接管
	}
	for _, c := range cases {
		if got := taskExec(c.m); got != c.want {
			t.Fatalf("taskExec(%v)=%s, 期望 %s", c.m, got, c.want)
		}
	}
}
