package store

import (
	"database/sql"
	"log"
	"os"
	"time"

	"go-monitor/collector"
	"go-monitor/config"

	_ "modernc.org/sqlite"
)

type DB struct {
	db *sql.DB
}

type DailyNetwork struct {
	ID        int    `json:"id"`
	Date      string `json:"date"`
	Upload    int64  `json:"upload"`
	Download  int64  `json:"download"`
	CreatedAt int64  `json:"created_at"`
}

type MonthlyNetwork struct {
	ID        int    `json:"id"`
	YearMonth string `json:"year_month"`
	Upload    int64  `json:"upload"`
	Download  int64  `json:"download"`
	CreatedAt int64  `json:"created_at"`
}

func NewDB(path string) (*DB, error) {
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
		return nil, err
	}

	s := &DB{db: db}
	if err := s.init(); err != nil {
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

	// Drop legacy hand-rolled indexes — UNIQUE() already provides them.
	_, _ = s.db.Exec(`DROP INDEX IF EXISTS idx_daily_network_date`)
	_, _ = s.db.Exec(`DROP INDEX IF EXISTS idx_monthly_network_year_month`)
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

func (s *DB) SaveHourlyNetwork(upload, download int64, loc *time.Location) error {
	if loc == nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	date := now.Format("2006-01-02")

	_, err := s.db.Exec(`
		INSERT INTO daily_network (date, upload, download, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(date) DO UPDATE SET
			upload = daily_network.upload + excluded.upload,
			download = daily_network.download + excluded.download,
			created_at = excluded.created_at
	`, date, upload, download, now.Unix())

	return err
}

func (s *DB) GetDailyNetwork(startDate, endDate string, limit int) ([]DailyNetwork, error) {
	query := `SELECT id, date, upload, download, created_at FROM daily_network WHERE date >= ? AND date <= ? ORDER BY date DESC`
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
		if err := rows.Scan(&d.ID, &d.Date, &d.Upload, &d.Download, &d.CreatedAt); err != nil {
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

	var totalUpload, totalDownload int64
	err := s.db.QueryRow(`
		SELECT COALESCE(SUM(upload), 0), COALESCE(SUM(download), 0)
		FROM daily_network
		WHERE date >= ? AND date <= ?
	`, startDate, endDate).Scan(&totalUpload, &totalDownload)

	if err != nil {
		return err
	}

	if totalUpload == 0 && totalDownload == 0 {
		return nil
	}

	_, err = s.db.Exec(`
		INSERT INTO monthly_network (year_month, upload, download, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(year_month) DO UPDATE SET
			upload = excluded.upload,
			download = excluded.download,
			created_at = excluded.created_at
	`, yearMonth, totalUpload, totalDownload, now.Unix())

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
	query := `SELECT id, year_month, upload, download, created_at FROM monthly_network WHERE year_month >= ? AND year_month <= ? ORDER BY year_month DESC`
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
		if err := rows.Scan(&m.ID, &m.YearMonth, &m.Upload, &m.Download, &m.CreatedAt); err != nil {
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

func (s *DB) runHourlyTasks(cfg *config.Config) {
	loc := aggregationLocation(cfg)

	upload, download := collector.GetHourlyTotalsAndReset()
	if upload > 0 || download > 0 {
		if err := s.SaveHourlyNetwork(upload, download, loc); err != nil {
			log.Println("保存每小时网络数据失败:", err)
		}
	}

	if err := s.SaveMonthlyNetwork(loc); err != nil {
		log.Println("保存月度网络汇总失败:", err)
	}

	snap := cfg.Snapshot()
	retentionDays := snap.Alert.RetentionDays
	retentionMonths := snap.Alert.MonthlyRetentionMonths
	if retentionDays > 0 || retentionMonths > 0 {
		if err := s.CleanOldData(retentionDays, retentionMonths); err != nil {
			log.Println("清理历史数据失败:", err)
		} else {
			log.Printf("已清理 %d 天/%d 月以前的历史数据", retentionDays, retentionMonths)
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
