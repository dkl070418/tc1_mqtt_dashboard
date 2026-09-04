package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type PowerSample struct {
	MAC       string
	TS        int64
	PowerW    float64
	IntervalS float64 // 本次采样距上一条的时间间隔（秒），用于能量积分
}

// TaskLogRecord 插口状态翻转记录，作为定时任务执行日志的原始数据。
// 注意：固件不主动上报"任务已触发"，此处只记录真实发生的状态变化，
// 是否由定时任务触发由查询接口结合任务配置推断。
type TaskLogRecord struct {
	MAC     string
	PlugIdx int
	PrevOn  int
	CurOn   int
	TS      int64
}

type PowerStore struct {
	db        *sql.DB
	mu        sync.Mutex
	insertCh  chan PowerSample
	taskLogCh chan TaskLogRecord
	done      chan struct{}
}

func NewPowerStore(dbPath string) (*PowerStore, error) {
	dir := filepath.Dir(dbPath)
	if err := mkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("创建数据库目录失败: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &PowerStore{
		db:        db,
		insertCh:  make(chan PowerSample, 5000),
		taskLogCh: make(chan TaskLogRecord, 1000),
		done:      make(chan struct{}),
	}
	if err := store.initSchema(); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.repairHourlyAgg(); err != nil {
		log.Printf("警告: 重建小时聚合失败: %v", err)
	}
	go store.insertLoop()
	go store.taskLogLoop()
	go store.aggLoop()
	go store.cleanupLoop()
	return store, nil
}

func (s *PowerStore) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS power_samples (
		mac TEXT NOT NULL,
		ts INTEGER NOT NULL,
		power_w REAL NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_power_samples_mac_ts ON power_samples(mac, ts);
	CREATE TABLE IF NOT EXISTS power_agg_1min (
		mac TEXT NOT NULL,
		bucket_ts INTEGER NOT NULL,
		avg_w REAL NOT NULL DEFAULT 0,
		max_w REAL NOT NULL DEFAULT 0,
		min_w REAL NOT NULL DEFAULT 0,
		energy_wh REAL NOT NULL DEFAULT 0,
		sample_count INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (mac, bucket_ts)
	);
	CREATE TABLE IF NOT EXISTS power_agg_1h (
		mac TEXT NOT NULL,
		bucket_ts INTEGER NOT NULL,
		avg_w REAL NOT NULL DEFAULT 0,
		max_w REAL NOT NULL DEFAULT 0,
		min_w REAL NOT NULL DEFAULT 0,
		energy_kwh REAL NOT NULL DEFAULT 0,
		sample_count INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (mac, bucket_ts)
	);
	CREATE TABLE IF NOT EXISTS task_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		mac TEXT NOT NULL,
		plug_idx INTEGER NOT NULL,
		prev_on INTEGER NOT NULL,
		cur_on INTEGER NOT NULL,
		ts INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_task_logs_mac_ts ON task_logs(mac, ts);
	CREATE TABLE IF NOT EXISTS store_meta (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);`
	_, err := s.db.Exec(schema)
	return err
}

// repairHourlyAgg 一次性重建 power_agg_1h。
// 旧版 rollupHourly 用 ON CONFLICT 累加且窗口重叠，导致同一分钟桶被重复计入、
// 小时能耗/样本数虚高。v1.9.0 起聚合改为幂等重算，这里从 1min 表全量重建一次
// 被污染的存量数据（通过 store_meta 标记保证只跑一次）。
func (s *PowerStore) repairHourlyAgg() error {
	var done string
	_ = s.db.QueryRow("SELECT value FROM store_meta WHERE key = 'hourly_agg_v2'").Scan(&done)
	if done == "1" {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM power_agg_1h"); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO power_agg_1h(mac, bucket_ts, avg_w, max_w, min_w, energy_kwh, sample_count)
		SELECT mac, (bucket_ts / 3600) * 3600,
			AVG(avg_w), MAX(max_w), MIN(min_w),
			SUM(energy_wh) / 1000.0,
			SUM(sample_count)
		FROM power_agg_1min
		GROUP BY mac, (bucket_ts / 3600) * 3600`); err != nil {
		return err
	}
	if _, err := tx.Exec(
		"INSERT INTO store_meta(key, value) VALUES('hourly_agg_v2', '1') ON CONFLICT(key) DO UPDATE SET value = excluded.value"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("小时聚合表已从分钟聚合全量重建（修复旧版重复累加）")
	return nil
}

func (s *PowerStore) Insert(sample PowerSample) {
	select {
	case s.insertCh <- sample:
	default:
		log.Printf("警告: 写入通道已满，丢弃采样数据 mac=%s ts=%d", sample.MAC, sample.TS)
	}
}

// InsertTaskLog 异步写入插口状态翻转记录（通道满时丢弃，避免阻塞 MQTT 处理）
func (s *PowerStore) InsertTaskLog(rec TaskLogRecord) {
	select {
	case s.taskLogCh <- rec:
	default:
		log.Printf("警告: 任务日志通道已满，丢弃记录 mac=%s plug=%d ts=%d", rec.MAC, rec.PlugIdx, rec.TS)
	}
}

func (s *PowerStore) taskLogLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	var batch []TaskLogRecord

	flush := func() {
		if len(batch) == 0 {
			return
		}
		s.mu.Lock()
		err := s.flushTaskLogs(batch)
		s.mu.Unlock()
		if err != nil {
			log.Printf("任务日志批量写入失败: %v", err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case rec := <-s.taskLogCh:
			batch = append(batch, rec)
			if len(batch) >= 100 {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-s.done:
			flush()
			return
		}
	}
}

func (s *PowerStore) flushTaskLogs(batch []TaskLogRecord) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("INSERT INTO task_logs(mac, plug_idx, prev_on, cur_on, ts) VALUES(?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range batch {
		if _, err := stmt.Exec(r.MAC, r.PlugIdx, r.PrevOn, r.CurOn, r.TS); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *PowerStore) insertLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	var batch []PowerSample

	flush := func() {
		if len(batch) == 0 {
			return
		}
		s.mu.Lock()
		err := s.flushBatch(batch)
		s.mu.Unlock()
		if err != nil {
			log.Printf("批量写入失败: %v", err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case sample := <-s.insertCh:
			batch = append(batch, sample)
			if len(batch) >= 100 {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-s.done:
			flush()
			return
		}
	}
}

func (s *PowerStore) flushBatch(batch []PowerSample) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rawStmt, err := tx.Prepare("INSERT OR IGNORE INTO power_samples(mac, ts, power_w) VALUES(?, ?, ?)")
	if err != nil {
		return err
	}
	defer rawStmt.Close()

	aggStmt, err := tx.Prepare(`
		INSERT INTO power_agg_1min(mac, bucket_ts, avg_w, max_w, min_w, energy_wh, sample_count)
		VALUES(?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT(mac, bucket_ts) DO UPDATE SET
			avg_w = (avg_w * sample_count + excluded.avg_w) / (sample_count + 1),
			max_w = MAX(max_w, excluded.max_w),
			min_w = MIN(min_w, excluded.min_w),
			energy_wh = energy_wh + excluded.energy_wh,
			sample_count = sample_count + 1`)
	if err != nil {
		return err
	}
	defer aggStmt.Close()

	for _, s := range batch {
		if _, err := rawStmt.Exec(s.MAC, s.TS, s.PowerW); err != nil {
			return err
		}
		bucket := (s.TS / 60) * 60
		energyWh := s.PowerW * s.IntervalS / 3600.0
		if _, err := aggStmt.Exec(s.MAC, bucket, s.PowerW, s.PowerW, s.PowerW, energyWh); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *PowerStore) aggLoop() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.rollupHourly()
		case <-s.done:
			return
		}
	}
}

// rollupHourly 从分钟聚合重算最近 3 小时的小时聚合。
// 使用 INSERT OR REPLACE 幂等重算（而非旧版 ON CONFLICT 累加），
// 同一小时被多次执行结果一致，且迟到/补写的分钟数据能自动并入。
func (s *PowerStore) rollupHourly() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO power_agg_1h(mac, bucket_ts, avg_w, max_w, min_w, energy_kwh, sample_count)
		SELECT mac, (bucket_ts / 3600) * 3600,
			AVG(avg_w), MAX(max_w), MIN(min_w),
			SUM(energy_wh) / 1000.0,
			SUM(sample_count)
		FROM power_agg_1min
		WHERE bucket_ts >= strftime('%s', 'now', '-3 hours')
		GROUP BY mac, (bucket_ts / 3600) * 3600`)
	if err != nil {
		log.Printf("小时聚合失败: %v", err)
	}
}

func (s *PowerStore) cleanupLoop() {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.cleanup()
		case <-s.done:
			return
		}
	}
}

func (s *PowerStore) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 原始秒级采样：保留 7 天
	cutoff := time.Now().AddDate(0, 0, -7).Unix()
	result, err := s.db.Exec("DELETE FROM power_samples WHERE ts < ?", cutoff)
	if err != nil {
		log.Printf("清理原始数据失败: %v", err)
		return
	}
	if n, _ := result.RowsAffected(); n > 0 {
		log.Printf("清理了 %d 条 7 天前的原始采样数据", n)
	}

	// 分钟聚合：保留 6 个月，防止数据库无限增长
	aggCutoff := time.Now().AddDate(0, -6, 0).Unix()
	result2, err := s.db.Exec("DELETE FROM power_agg_1min WHERE bucket_ts < ?", aggCutoff)
	if err != nil {
		log.Printf("清理分钟聚合数据失败: %v", err)
		return
	}
	if n, _ := result2.RowsAffected(); n > 0 {
		log.Printf("清理了 %d 条 6 个月前的分钟聚合数据", n)
	}

	// 任务执行日志：保留 15 天
	taskCutoff := time.Now().AddDate(0, 0, -15).Unix()
	result3, err := s.db.Exec("DELETE FROM task_logs WHERE ts < ?", taskCutoff)
	if err != nil {
		log.Printf("清理任务日志失败: %v", err)
		return
	}
	if n, _ := result3.RowsAffected(); n > 0 {
		log.Printf("清理了 %d 条 15 天前的任务执行日志", n)
	}
}

// QueryTaskLogs 查询插口状态翻转记录（时间倒序）
func (s *PowerStore) QueryTaskLogs(mac string, plugIdx *int, limit int) ([]TaskLogRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := "SELECT ts, plug_idx, prev_on, cur_on FROM task_logs WHERE mac = ?"
	args := []any{mac}
	if plugIdx != nil {
		query += " AND plug_idx = ?"
		args = append(args, *plugIdx)
	}
	query += " ORDER BY ts DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TaskLogRecord
	for rows.Next() {
		var r TaskLogRecord
		r.MAC = mac
		if err := rows.Scan(&r.TS, &r.PlugIdx, &r.PrevOn, &r.CurOn); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *PowerStore) QueryPower(mac string, from, to int64, granularity string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var query string
	switch granularity {
	case "raw":
		query = `SELECT ts, power_w FROM power_samples
			WHERE mac = ? AND power_w >= 0 AND ts >= ? AND ts <= ?
			ORDER BY ts ASC LIMIT 100000`
	case "1h", "hour":
		query = `SELECT bucket_ts, avg_w, max_w, min_w, energy_kwh
			FROM power_agg_1h
			WHERE mac = ? AND bucket_ts >= ? AND bucket_ts <= ?
			ORDER BY bucket_ts ASC`
	default:
		query = `SELECT bucket_ts, avg_w, max_w, min_w, energy_wh
			FROM power_agg_1min
			WHERE mac = ? AND bucket_ts >= ? AND bucket_ts <= ?
			ORDER BY bucket_ts ASC`
	}
	rows, err := s.db.Query(query, mac, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	var result []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any)
		for i, col := range cols {
			row[col] = vals[i]
		}
		result = append(result, row)
	}
	return result, nil
}

func (s *PowerStore) GetSummary(mac string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	summary := map[string]any{
		"mac": mac,
	}

	loc := time.FixedZone("CST", 8*3600)
	now := time.Now().In(loc)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).Unix()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc).Unix()

	var currentPower sql.NullFloat64
	s.db.QueryRow(`SELECT power_w FROM power_samples
		WHERE mac = ? AND power_w >= 0 ORDER BY ts DESC LIMIT 1`, mac).Scan(&currentPower)
	summary["current_power_w"] = currentPower.Float64

	row := s.db.QueryRow(`SELECT COALESCE(SUM(energy_wh), 0) / 1000.0
		FROM power_agg_1min WHERE mac = ? AND bucket_ts >= ?`, mac, todayStart)
	var todayKwh float64
	row.Scan(&todayKwh)
	summary["today_energy_kwh"] = todayKwh

	row = s.db.QueryRow(`SELECT COALESCE(SUM(energy_kwh), 0)
		FROM power_agg_1h WHERE mac = ? AND bucket_ts >= ?`, mac, monthStart)
	var monthKwh float64
	row.Scan(&monthKwh)
	summary["month_energy_kwh"] = monthKwh

	return summary, nil
}

func (s *PowerStore) Close() {
	close(s.done)
	close(s.insertCh)
	close(s.taskLogCh)
	s.db.Close()
}

func mkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}
