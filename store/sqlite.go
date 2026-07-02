package store

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/lyj404/go-monitor/collector"
	"github.com/lyj404/go-monitor/config"

	_ "modernc.org/sqlite"
)

type DB struct {
	db *sql.DB
}

type DailyNetwork struct {
	ID          int    `json:"id"`
	Date        string `json:"date"`
	Upload      int64  `json:"upload"`
	Download    int64  `json:"download"`
	LanUpload   int64  `json:"lan_upload,omitempty"`
	LanDownload int64  `json:"lan_download,omitempty"`
	WanUpload   int64  `json:"wan_upload,omitempty"`
	WanDownload int64  `json:"wan_download,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

type MonthlyNetwork struct {
	ID          int    `json:"id"`
	YearMonth   string `json:"year_month"`
	Upload      int64  `json:"upload"`
	Download    int64  `json:"download"`
	LanUpload   int64  `json:"lan_upload,omitempty"`
	LanDownload int64  `json:"lan_download,omitempty"`
	WanUpload   int64  `json:"wan_upload,omitempty"`
	WanDownload int64  `json:"wan_download,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

type AlertRecord struct {
	ID           int    `json:"id"`
	AlertType    string `json:"alert_type"`
	Message      string `json:"message"`
	CurrentValue string `json:"current_value"`
	Threshold    string `json:"threshold"`
	CreatedAt    int64  `json:"created_at"`
}

type DailyMetrics struct {
	ID          int     `json:"id"`
	Date        string  `json:"date"`
	AvgCPU      float64 `json:"avg_cpu"`
	MaxCPU      float64 `json:"max_cpu"`
	AvgMemory   float64 `json:"avg_memory"`
	MaxMemory   float64 `json:"max_memory"`
	AvgDisk     float64 `json:"avg_disk"`
	MaxDisk     float64 `json:"max_disk"`
	SampleCount int     `json:"sample_count"`
	CreatedAt   int64   `json:"created_at"`
}

func NewDB(path string) (*DB, error) {
	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		return nil, fmt.Errorf("database path must be absolute: %q", path)
	}
	path = cleanPath

	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(path, 0755); err != nil {
			return nil, err
		}
	}

	dbPath := path + "/monitor.db"
	// busy_timeout lets readers/writers wait briefly instead of failing
	// hard when the writer holds the lock; combined with WAL it gives us
	// concurrent reads without losing crash safety.
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}

	// Single writer is still required by SQLite. Allow more idle/open
	// connections so reads can run concurrently in WAL mode.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(time.Hour)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	s := &DB{db: db}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}

	return s, nil
}

func (s *DB) init() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS daily_network (
			id INTEGER PRIMARY KEY,
			date TEXT NOT NULL,
			upload INTEGER NOT NULL,
			download INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			UNIQUE(date)
		)
	`)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS monthly_network (
			id INTEGER PRIMARY KEY,
			year_month TEXT NOT NULL,
			upload INTEGER NOT NULL,
			download INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			UNIQUE(year_month)
		)
	`)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS alert_history (
			id INTEGER PRIMARY KEY,
			alert_type TEXT NOT NULL,
			message TEXT NOT NULL,
			current_value TEXT NOT NULL,
			threshold TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)
	`)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS daily_metrics (
			id INTEGER PRIMARY KEY,
			date TEXT NOT NULL,
			avg_cpu REAL NOT NULL DEFAULT 0,
			max_cpu REAL NOT NULL DEFAULT 0,
			avg_memory REAL NOT NULL DEFAULT 0,
			max_memory REAL NOT NULL DEFAULT 0,
			avg_disk REAL NOT NULL DEFAULT 0,
			max_disk REAL NOT NULL DEFAULT 0,
			sample_count INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			UNIQUE(date)
		)
	`)
	if err != nil {
		return err
	}

	// Drop legacy hand-rolled indexes — UNIQUE() already provides them.
	_, _ = s.db.Exec(`DROP INDEX IF EXISTS idx_daily_network_date`)
	_, _ = s.db.Exec(`DROP INDEX IF EXISTS idx_monthly_network_year_month`)

	// Migration: add LAN/WAN columns for existing databases.
	migrateCols := []string{
		"ALTER TABLE daily_network ADD COLUMN lan_upload INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE daily_network ADD COLUMN lan_download INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE daily_network ADD COLUMN wan_upload INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE daily_network ADD COLUMN wan_download INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE monthly_network ADD COLUMN lan_upload INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE monthly_network ADD COLUMN lan_download INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE monthly_network ADD COLUMN wan_upload INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE monthly_network ADD COLUMN wan_download INTEGER NOT NULL DEFAULT 0",
	}
	for _, m := range migrateCols {
		_, _ = s.db.Exec(m)
	}

	return nil
}

func aggregationLocation(cfg *config.Config) *time.Location {
	if cfg != nil {
		if tz := cfg.Snapshot().Server.Timezone; tz != "" {
			if loc, err := time.LoadLocation(tz); err == nil {
				return loc
			}
			log.Printf("无效的时区配置 %q，回退到 UTC", tz)
		}
	}
	return time.UTC
}

func (s *DB) SaveHourlyNetwork(upload, download, lanUp, lanDown, wanUp, wanDown int64, loc *time.Location) error {
	if loc == nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	date := now.Format("2006-01-02")

	_, err := s.db.Exec(`
		INSERT INTO daily_network (date, upload, download, lan_upload, lan_download, wan_upload, wan_download, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(date) DO UPDATE SET
			upload = daily_network.upload + excluded.upload,
			download = daily_network.download + excluded.download,
			lan_upload = daily_network.lan_upload + excluded.lan_upload,
			lan_download = daily_network.lan_download + excluded.lan_download,
			wan_upload = daily_network.wan_upload + excluded.wan_upload,
			wan_download = daily_network.wan_download + excluded.wan_download,
			created_at = excluded.created_at
	`, date, upload, download, lanUp, lanDown, wanUp, wanDown, now.Unix())

	return err
}

func (s *DB) GetDailyNetwork(startDate, endDate string, limit int) ([]DailyNetwork, error) {
	query := `SELECT id, date, upload, download, COALESCE(lan_upload,0), COALESCE(lan_download,0), COALESCE(wan_upload,0), COALESCE(wan_download,0), created_at FROM daily_network WHERE date >= ? AND date <= ? ORDER BY date DESC`
	args := []interface{}{startDate, endDate}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []DailyNetwork
	for rows.Next() {
		var d DailyNetwork
		if err := rows.Scan(&d.ID, &d.Date, &d.Upload, &d.Download, &d.LanUpload, &d.LanDownload, &d.WanUpload, &d.WanDownload, &d.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *DB) SaveMonthlyNetwork(loc *time.Location) error {
	if loc == nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	yearMonth := now.Format("2006-01")
	startDate := yearMonth + "-01"
	endDate := now.Format("2006-01-02")

	var totalUpload, totalDownload, totalLanUp, totalLanDown, totalWanUp, totalWanDown int64
	err := s.db.QueryRow(`
		SELECT COALESCE(SUM(upload), 0), COALESCE(SUM(download), 0),
		       COALESCE(SUM(lan_upload), 0), COALESCE(SUM(lan_download), 0),
		       COALESCE(SUM(wan_upload), 0), COALESCE(SUM(wan_download), 0)
		FROM daily_network
		WHERE date >= ? AND date <= ?
	`, startDate, endDate).Scan(&totalUpload, &totalDownload, &totalLanUp, &totalLanDown, &totalWanUp, &totalWanDown)

	if err != nil {
		return err
	}

	_, err = s.db.Exec(`
		INSERT INTO monthly_network (year_month, upload, download, lan_upload, lan_download, wan_upload, wan_download, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(year_month) DO UPDATE SET
			upload = excluded.upload,
			download = excluded.download,
			lan_upload = excluded.lan_upload,
			lan_download = excluded.lan_download,
			wan_upload = excluded.wan_upload,
			wan_download = excluded.wan_download,
			created_at = excluded.created_at
	`, yearMonth, totalUpload, totalDownload, totalLanUp, totalLanDown, totalWanUp, totalWanDown, now.Unix())

	return err
}

func (s *DB) Close() error {
	return s.db.Close()
}

func (s *DB) StartHourlyTasks(stopCh <-chan struct{}, cfg *config.Config) {
	go func() {
		timer := time.NewTimer(time.Until(nextHour(time.Now())))
		defer timer.Stop()

		var ticker *time.Ticker
		var tickerC <-chan time.Time
		defer func() {
			if ticker != nil {
				ticker.Stop()
			}
		}()

		for {
			select {
			case <-timer.C:
				s.runHourlyTasks(cfg)
				// Start the steady-state ticker only after the first
				// aligned tick so we don't fire twice in the first hour.
				if ticker == nil {
					ticker = time.NewTicker(time.Hour)
					tickerC = ticker.C
				}
			case <-tickerC:
				s.runHourlyTasks(cfg)
			case <-stopCh:
				return
			}
		}
	}()
}

func (s *DB) GetMonthlyNetwork(startMonth, endMonth string, limit int) ([]MonthlyNetwork, error) {
	query := `SELECT id, year_month, upload, download, COALESCE(lan_upload,0), COALESCE(lan_download,0), COALESCE(wan_upload,0), COALESCE(wan_download,0), created_at FROM monthly_network WHERE year_month >= ? AND year_month <= ? ORDER BY year_month DESC`
	args := []interface{}{startMonth, endMonth}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []MonthlyNetwork
	for rows.Next() {
		var m MonthlyNetwork
		if err := rows.Scan(&m.ID, &m.YearMonth, &m.Upload, &m.Download, &m.LanUpload, &m.LanDownload, &m.WanUpload, &m.WanDownload, &m.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *DB) CleanOldData(retentionDays, retentionMonths int) error {
	if retentionDays > 0 {
		if _, err := s.db.Exec(
			`DELETE FROM daily_network WHERE date < date('now', ?)`,
			"-"+itoa(retentionDays)+" days",
		); err != nil {
			return err
		}
	}

	if retentionMonths > 0 {
		if _, err := s.db.Exec(
			`DELETE FROM monthly_network WHERE year_month < strftime('%Y-%m', 'now', ?)`,
			"-"+itoa(retentionMonths)+" months",
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *DB) SaveAlert(alertType, message, currentValue, threshold string) error {
	_, err := s.db.Exec(
		`INSERT INTO alert_history (alert_type, message, current_value, threshold, created_at) VALUES (?, ?, ?, ?, ?)`,
		alertType, message, currentValue, threshold, time.Now().Unix(),
	)
	return err
}

func (s *DB) GetAlertHistory(limit int) ([]AlertRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, alert_type, message, current_value, threshold, created_at FROM alert_history ORDER BY id DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []AlertRecord
	for rows.Next() {
		var a AlertRecord
		if err := rows.Scan(&a.ID, &a.AlertType, &a.Message, &a.CurrentValue, &a.Threshold, &a.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

func (s *DB) CleanOldAlerts(retentionDays int) error {
	if retentionDays > 0 {
		_, err := s.db.Exec(
			`DELETE FROM alert_history WHERE created_at < ?`,
			time.Now().AddDate(0, 0, -retentionDays).Unix(),
		)
		return err
	}
	return nil
}

func (s *DB) SaveDailyMetrics(avgCPU, maxCPU, avgMem, maxMem, avgDisk, maxDisk float64, sampleCount int, loc *time.Location) error {
	if loc == nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	date := now.Format("2006-01-02")

	_, err := s.db.Exec(`
		INSERT INTO daily_metrics (date, avg_cpu, max_cpu, avg_memory, max_memory, avg_disk, max_disk, sample_count, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(date) DO UPDATE SET
			avg_cpu = (daily_metrics.avg_cpu * daily_metrics.sample_count + excluded.avg_cpu * excluded.sample_count) / (daily_metrics.sample_count + excluded.sample_count),
			max_cpu = MAX(daily_metrics.max_cpu, excluded.max_cpu),
			avg_memory = (daily_metrics.avg_memory * daily_metrics.sample_count + excluded.avg_memory * excluded.sample_count) / (daily_metrics.sample_count + excluded.sample_count),
			max_memory = MAX(daily_metrics.max_memory, excluded.max_memory),
			avg_disk = (daily_metrics.avg_disk * daily_metrics.sample_count + excluded.avg_disk * excluded.sample_count) / (daily_metrics.sample_count + excluded.sample_count),
			max_disk = MAX(daily_metrics.max_disk, excluded.max_disk),
			sample_count = daily_metrics.sample_count + excluded.sample_count,
			created_at = excluded.created_at
	`, date, avgCPU, maxCPU, avgMem, maxMem, avgDisk, maxDisk, sampleCount, now.Unix())
	return err
}

func (s *DB) GetDailyMetrics(startDate, endDate string, limit int) ([]DailyMetrics, error) {
	query := `SELECT id, date, avg_cpu, max_cpu, avg_memory, max_memory, avg_disk, max_disk, sample_count, created_at FROM daily_metrics WHERE date >= ? AND date <= ? ORDER BY date DESC`
	args := []any{startDate, endDate}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []DailyMetrics
	for rows.Next() {
		var d DailyMetrics
		if err := rows.Scan(&d.ID, &d.Date, &d.AvgCPU, &d.MaxCPU, &d.AvgMemory, &d.MaxMemory, &d.AvgDisk, &d.MaxDisk, &d.SampleCount, &d.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

func (s *DB) CleanOldMetrics(retentionDays int) error {
	if retentionDays > 0 {
		_, err := s.db.Exec(
			`DELETE FROM daily_metrics WHERE date < date('now', ?)`,
			"-"+itoa(retentionDays)+" days",
		)
		return err
	}
	return nil
}

func (s *DB) runHourlyTasks(cfg *config.Config) {
	loc := aggregationLocation(cfg)

	upload, download, lanUp, lanDown, wanUp, wanDown := collector.GetHourlyTotalsAndReset()
	if err := s.SaveHourlyNetwork(upload, download, lanUp, lanDown, wanUp, wanDown, loc); err != nil {
		log.Println("保存每小时网络数据失败:", err)
	}

	if err := s.SaveMonthlyNetwork(loc); err != nil {
		log.Println("保存月度网络汇总失败:", err)
	}

	// Save hourly metrics aggregation
	hm := collector.GetHourlyMetricsAndReset()
	if hm.SampleCount > 0 {
		if err := s.SaveDailyMetrics(hm.AvgCPU, hm.MaxCPU, hm.AvgMemory, hm.MaxMemory, hm.AvgDisk, hm.MaxDisk, hm.SampleCount, loc); err != nil {
			log.Println("保存每日指标数据失败:", err)
		}
	}

	snap := cfg.Snapshot()
	retentionDays := snap.Alert.RetentionDays
	retentionMonths := snap.Alert.MonthlyRetentionMonths
	alertRetentionDays := snap.Alert.AlertRetentionDays
	metricsRetentionDays := snap.Alert.MetricsRetentionDays

	// Use specific retention days if configured, otherwise fall back to general retention days
	if alertRetentionDays <= 0 {
		alertRetentionDays = retentionDays
	}
	if metricsRetentionDays <= 0 {
		metricsRetentionDays = retentionDays
	}

	if retentionDays > 0 || retentionMonths > 0 {
		if err := s.CleanOldData(retentionDays, retentionMonths); err != nil {
			log.Println("清理历史数据失败:", err)
		} else {
			log.Printf("已清理 %d 天/%d 月以前的网络流量数据", retentionDays, retentionMonths)
		}
	}

	// Clean old alerts with specific retention days
	if alertRetentionDays > 0 {
		if err := s.CleanOldAlerts(alertRetentionDays); err != nil {
			log.Println("清理告警历史失败:", err)
		} else {
			log.Printf("已清理 %d 天以前的告警历史", alertRetentionDays)
		}
	}

	// Clean old metrics with specific retention days
	if metricsRetentionDays > 0 {
		if err := s.CleanOldMetrics(metricsRetentionDays); err != nil {
			log.Println("清理指标历史失败:", err)
		} else {
			log.Printf("已清理 %d 天以前的指标历史", metricsRetentionDays)
		}
	}
}

func nextHour(now time.Time) time.Time {
	return now.Truncate(time.Hour).Add(time.Hour)
}

// itoa is a small allocation-free helper for building SQLite date modifiers.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
