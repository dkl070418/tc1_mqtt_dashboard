package main

import (
	"path/filepath"
	"testing"
	"time"
)

// TestIntegralInterval 能量积分的间隔上限必须是自适应的：
// 2 秒级上报维持旧行为（30 秒上限），分钟级上报要放大上限，断线超长间隔仍被挡住。
func TestIntegralInterval(t *testing.T) {
	rep := func(v float64, n int) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = v
		}
		return out
	}
	cases := []struct {
		name    string
		gap     float64
		history []float64
		want    float64
	}{
		{"首次采样按 2 秒", 2, nil, 2},
		{"亚秒级夹到 0.5", 0.1, nil, 0.5},
		{"2 秒级上报不受影响", 2, rep(2, gapHistoryLen), 2},
		{"3 秒级上报不受影响", 3, rep(3, gapHistoryLen), 3},
		{"30 秒级上报：上限仍是 30", 30, rep(2, gapHistoryLen), 30},
		{"60 秒级上报：上限放大到 180，不再被砍", 60, rep(60, gapHistoryLen), 60},
		{"稀疏源 + 超长间隔：按 3×中位数挡住", 5000, rep(60, gapHistoryLen), 180},
		{"断线 2 小时 + 2 秒级设备：按 30 秒计", 7200, rep(2, gapHistoryLen), 30},
		{"上限天花板 1 小时", 100000, rep(10000, gapHistoryLen), 3600},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := integralInterval(c.gap, c.history); got != c.want {
				t.Fatalf("integralInterval(%v, 中位数%v)=%v, 期望 %v",
					c.gap, medianFloat(c.history), got, c.want)
			}
		})
	}
}

// TestParsePowerW 功率字段可能是数字或字符串；没有该字段时要能识别出来
// （v1 的 state、v2 的 HA power/state 都是这两种形态）。
func TestParsePowerW(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		want    float64
		wantOK  bool
	}{
		{"字符串功率（V2 HA topic）", map[string]any{"power": "30.098"}, 30.098, true},
		{"整数功率", map[string]any{"power": float64(33)}, 33, true},
		{"小数功率", map[string]any{"power": 45.6}, 45.6, true},
		{"负值功率（芯片异常）", map[string]any{"power": "-1"}, -1, true},
		{"没有功率字段（纯状态消息）", map[string]any{"mac": "a1b2c3d4e5f6"}, 0, false},
		{"功率字段类型不认识", map[string]any{"power": true}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parsePowerW(c.payload)
			if ok != c.wantOK || (ok && got != c.want) {
				t.Fatalf("parsePowerW(%v)=(%v,%v), 期望 (%v,%v)", c.payload, got, ok, c.want, c.wantOK)
			}
		})
	}
}

// TestRollupHourlyBackfillsMissingHours 小时聚合必须能补算"分钟有、小时没有"的历史小时
// （容器重启/停机超过 3 小时会造成这种空洞，"本月用电"读的就是 1h 表）。
func TestRollupHourlyBackfillsMissingHours(t *testing.T) {
	store, err := NewPowerStore(filepath.Join(t.TempDir(), "power.db"))
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer store.Close()

	const mac = "a1b2c3d4e5f6"
	// 10 小时前那一小时：故意只在 1min 里有数据（模拟"小时聚合缺了这个小时"）
	oldHour := (time.Now().Unix()/3600)*3600 - 10*3600
	// 另一小时预置一行 1h 记录，验证补算不会破坏已有数据
	keptHour := oldHour - 3600

	if _, err := store.db.Exec(`
		INSERT INTO power_agg_1min(mac, bucket_ts, avg_w, max_w, min_w, energy_wh, sample_count)
		VALUES(?, ?, 10, 12, 8, 5, 60), (?, ?, 20, 22, 18, 10, 60)`,
		mac, oldHour+60, mac, oldHour+120); err != nil {
		t.Fatalf("写 1min 失败: %v", err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO power_agg_1h(mac, bucket_ts, avg_w, max_w, min_w, energy_kwh, sample_count)
		VALUES(?, ?, 99, 99, 99, 9.9, 9)`, mac, keptHour); err != nil {
		t.Fatalf("写 1h 失败: %v", err)
	}

	store.rollupHourly()

	// 缺失的那个小时应被补上，且能量 = (5+10)/1000 kWh
	var avg, maxW, minW, kwh float64
	var cnt int
	if err := store.db.QueryRow(`
		SELECT avg_w, max_w, min_w, energy_kwh, sample_count FROM power_agg_1h
		WHERE mac=? AND bucket_ts=?`, mac, oldHour).Scan(&avg, &maxW, &minW, &kwh, &cnt); err != nil {
		t.Fatalf("补算后仍找不到该小时: %v", err)
	}
	if avg != 15 || maxW != 22 || minW != 8 || cnt != 120 {
		t.Fatalf("补算内容不对 avg=%v max=%v min=%v cnt=%d", avg, maxW, minW, cnt)
	}
	if kwh < 0.0149 || kwh > 0.0151 {
		t.Fatalf("能量应为 0.015 kWh, 实得 %v", kwh)
	}

	// 已有小时不能被改坏
	var kept float64
	if err := store.db.QueryRow(`SELECT energy_kwh FROM power_agg_1h WHERE mac=? AND bucket_ts=?`,
		mac, keptHour).Scan(&kept); err != nil {
		t.Fatalf("查询已有小时失败: %v", err)
	}
	if kept != 9.9 {
		t.Fatalf("已有小时被改了: %v", kept)
	}
}

// TestRecomputeAllHoursFixesStaleHours 全量重算必须按分钟数据纠正"陈旧的小时行"
// （旧版只重算最近 3 小时，更早的错行会一直留着）。
func TestRecomputeAllHoursFixesStaleHours(t *testing.T) {
	store, err := NewPowerStore(filepath.Join(t.TempDir(), "power.db"))
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer store.Close()

	const mac = "a1b2c3d4e5f6"
	oldHour := (time.Now().Unix()/3600)*3600 - 30*3600
	if _, err := store.db.Exec(`
		INSERT INTO power_agg_1min(mac, bucket_ts, avg_w, max_w, min_w, energy_wh, sample_count)
		VALUES(?, ?, 10, 12, 8, 5, 60), (?, ?, 20, 22, 18, 10, 60)`,
		mac, oldHour+60, mac, oldHour+120); err != nil {
		t.Fatalf("写 1min 失败: %v", err)
	}
	// 陈旧行：与分钟数据不一致
	if _, err := store.db.Exec(`
		INSERT INTO power_agg_1h(mac, bucket_ts, avg_w, max_w, min_w, energy_kwh, sample_count)
		VALUES(?, ?, 1, 1, 1, 0.001, 1)`, mac, oldHour); err != nil {
		t.Fatalf("写陈旧 1h 失败: %v", err)
	}

	store.recomputeAllHours()

	var kwh float64
	var cnt int
	if err := store.db.QueryRow(`SELECT energy_kwh, sample_count FROM power_agg_1h WHERE mac=? AND bucket_ts=?`,
		mac, oldHour).Scan(&kwh, &cnt); err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if kwh < 0.0149 || kwh > 0.0151 || cnt != 120 {
		t.Fatalf("陈旧行未被纠正: energy=%v cnt=%d", kwh, cnt)
	}
}
