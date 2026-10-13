package main

import (
	"testing"
	"time"
)

var testLoc = time.FixedZone("CST", 8*3600)

func mustDate(t *testing.T, ymd string) time.Time {
	t.Helper()
	d, err := time.ParseInLocation("2006-01-02 15:04:05", ymd, testLoc)
	if err != nil {
		t.Fatalf("解析时间失败 %s: %v", ymd, err)
	}
	return d
}

func testTask(hour, minute, repeat int) taskSpec {
	return taskSpec{mac: "d0bae4637be5", plugIdx: 1, taskKey: "task_0", hour: hour, minute: minute, repeat: repeat, action: 1}
}

// 2026-10-12 周一 / 10-13 周二 / 10-17 周六 / 10-18 周日
func TestDueOccurrence(t *testing.T) {
	tuesdayBit1 := 1 << 1 // 工作日掩码里只点亮周二
	cases := []struct {
		name    string
		now     string
		hour    int
		minute  int
		repeat  int
		window  time.Duration
		wantOK  bool
		wantOcc string
	}{
		{name: "当分钟内(秒=30)按时", now: "2026-10-13 08:00:30", hour: 8, minute: 0, repeat: 0, window: time.Minute, wantOK: true, wantOcc: "2026-10-13 08:00:00"},
		{name: "当分钟内(秒=59)按时", now: "2026-10-13 08:00:59", hour: 8, minute: 0, repeat: 0, window: time.Minute, wantOK: true, wantOcc: "2026-10-13 08:00:00"},
		{name: "满一分钟即过期(窗口1分钟)", now: "2026-10-13 08:01:00", hour: 8, minute: 0, repeat: 0, window: time.Minute, wantOK: false},
		{name: "迟到5分钟在31分钟窗口内补发", now: "2026-10-13 08:05:00", hour: 8, minute: 0, repeat: 0, window: 31 * time.Minute, wantOK: true, wantOcc: "2026-10-13 08:00:00"},
		{name: "迟到30分59秒仍可补发", now: "2026-10-13 08:30:59", hour: 8, minute: 0, repeat: 0, window: 31 * time.Minute, wantOK: true, wantOcc: "2026-10-13 08:00:00"},
		{name: "迟到31分钟不再补发", now: "2026-10-13 08:31:00", hour: 8, minute: 0, repeat: 0, window: 31 * time.Minute, wantOK: false},
		{name: "还没到点不提前发", now: "2026-10-13 07:59:59", hour: 8, minute: 0, repeat: 0, window: 31 * time.Minute, wantOK: false},
		{name: "昨天同一时刻不回看", now: "2026-10-13 07:00:00", hour: 8, minute: 0, repeat: 0, window: 31 * time.Minute, wantOK: false},
		{name: "工作日任务在周六不发", now: "2026-10-17 08:00:30", hour: 8, minute: 0, repeat: tuesdayBit1, window: 31 * time.Minute, wantOK: false},
		{name: "工作日任务在周二发", now: "2026-10-13 08:00:30", hour: 8, minute: 0, repeat: tuesdayBit1, window: 31 * time.Minute, wantOK: true, wantOcc: "2026-10-13 08:00:00"},
		{name: "周六凌晨补发周五夜的23:59", now: "2026-10-17 00:10:00", hour: 23, minute: 59, repeat: 31, window: 31 * time.Minute, wantOK: true, wantOcc: "2026-10-16 23:59:00"},
		{name: "跨零点超出窗口不发", now: "2026-10-18 00:31:00", hour: 23, minute: 59, repeat: 127, window: 31 * time.Minute, wantOK: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			occ, ok := dueOccurrence(testTask(c.hour, c.minute, c.repeat), mustDate(t, c.now), c.window)
			if ok != c.wantOK {
				t.Fatalf("ok=%v, 期望 %v (occ=%v)", ok, c.wantOK, occ)
			}
			if !ok {
				return
			}
			if got := occ.Format("2006-01-02 15:04:05"); got != c.wantOcc {
				t.Fatalf("应执行时刻=%s, 期望 %s", got, c.wantOcc)
			}
		})
	}
}

// TestFireMinuteKeyCompatibleWithLegacy 防重复键必须与旧版(now 格式化)同串,
// 否则按时触发过的任务在升级后会被补发第二次。
func TestFireMinuteKeyCompatibleWithLegacy(t *testing.T) {
	for _, sec := range []int{0, 17, 59} {
		now := time.Date(2026, time.October, 13, 8, 30, sec, 0, testLoc)
		occ, ok := dueOccurrence(testTask(8, 30, 0), now, time.Minute)
		if !ok {
			t.Fatalf("秒=%d 应当按时触发", sec)
		}
		if got, want := occ.Format("2006-01-02 15:04"), now.Format("2006-01-02 15:04"); got != want {
			t.Fatalf("fire_minute=%s, 旧版=%s, 必须一致", got, want)
		}
	}
}

// TestWindowOneEqualsLegacyRule 锁住回滚把手: scheduler_catchup_minutes=0 时窗口只有
// 1 分钟, 判定结果必须与旧版"当前小时+分钟匹配且星期命中"逐分钟等价。
func TestWindowOneEqualsLegacyRule(t *testing.T) {
	window := time.Minute
	for _, day := range []int{12, 13, 17, 18} { // 周一 周二 周六 周日
		for _, repeat := range []int{0, 31, 96, 127} {
			for h := 0; h < 24; h++ {
				for m := 0; m < 60; m++ {
					for _, sec := range []int{0, 59} {
						now := time.Date(2026, time.October, day, h, m, sec, 0, testLoc)
						tp := testTask(h, m, repeat)
						bit := (int(now.Weekday()) + 6) % 7
						legacy := tp.hour == now.Hour() && tp.minute == now.Minute() &&
							(tp.repeat == 0 || tp.repeat&(1<<bit) != 0)
						_, got := dueOccurrence(tp, now, window)
						if got != legacy {
							t.Fatalf("10-%02d %02d:%02d:%02d repeat=%d: 新=%v 旧=%v", day, h, m, sec, repeat, got, legacy)
						}
					}
				}
			}
		}
	}
}

func TestSchedulerCatchupMinutesDefaultAndClamp(t *testing.T) {
	old := cfg.SchedulerCatchupMinutes
	defer func() { cfg.SchedulerCatchupMinutes = old }()

	cfg.SchedulerCatchupMinutes = nil
	if got := schedulerCatchupMinutes(); got != 30 {
		t.Fatalf("未配置应为 30, 实得 %d", got)
	}
	for _, c := range []struct{ in, want int }{{0, 0}, {-5, 0}, {5, 5}, {120, 120}, {9999, 120}} {
		v := c.in
		cfg.SchedulerCatchupMinutes = &v
		if got := schedulerCatchupMinutes(); got != c.want {
			t.Fatalf("配置 %d 应为 %d, 实得 %d", c.in, c.want, got)
		}
	}
}
