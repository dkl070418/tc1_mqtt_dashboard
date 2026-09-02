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

type PowerStore struct {
	db       *sql.DB
	mu       sync.Mutex
	insertCh chan PowerSample
	done     chan struct{}
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
		db:       db,
		insertCh: make(chan PowerSample, 5000),
		done:     make(chan struct{}),
	}
	if err := store.initSchema(); err != nil {
		db.Close()
		return nil, err
	}
	go store.insertLoop()
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
	);`
	_, err := s.db.Exec(schema)
	return err
}

func (s *PowerStore) Insert(sample PowerSample) {
	select {
	case s.insertCh <- sample:
	default:
		log.Printf("警告: 写入通道已满，丢弃采样数据 mac=%s ts=%d", sample.MAC, sample.TS)
	}
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

func (s *PowerStore) rollupHourly() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`
		INSERT INTO power_agg_1h(mac, bucket_ts, avg_w, max_w, min_w, energy_kwh, sample_count)
		SELECT mac, (bucket_ts / 3600) * 3600,
			AVG(avg_w), MAX(max_w), MIN(min_w),
			SUM(energy_wh) / 1000.0,
			SUM(sample_count)
		FROM power_agg_1min
		WHERE bucket_ts >= strftime('%s', 'now', '-2 hours')
		GROUP BY mac, (bucket_ts / 3600) * 3600
		ON CONFLICT(mac, bucket_ts) DO UPDATE SET
			avg_w = (avg_w * sample_count + excluded.avg_w * excluded.sample_count) / (sample_count + excluded.sample_count),
			max_w = MAX(max_w, excluded.max_w),
			min_w = MIN(min_w, excluded.min_w),
			energy_kwh = energy_kwh + excluded.energy_kwh,
			sample_count = sample_count + excluded.sample_count`)
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
	s.db.Close()
}

func mkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}