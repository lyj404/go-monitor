package store

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	"go-monitor/collector"

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
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	// SQLite is single-writer; limit connection pool
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

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

	// Add indexes for query performance
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_daily_network_date ON daily_network(date)`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_monthly_network_year_month ON monthly_network(year_month)`)
	return err
}

func (s *DB) SaveHourlyNetwork(upload, download int64) error {
	loc, _ := time.LoadLocation("UTC")
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
	query := `SELECT id, date, upload, download, created_at FROM daily_network WHERE date >= ? AND date <= ?`
	if limit > 0 {
		query += fmt.Sprintf(" ORDER BY date DESC LIMIT %d", limit)
	} else {
		query += " ORDER BY date DESC"
	}
	rows, err := s.db.Query(query, startDate, endDate)
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
	return result, nil
}

func (s *DB) SaveMonthlyNetwork() error {
	loc, _ := time.LoadLocation("UTC")
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

func (s *DB) StartHourlyTasks(stopCh <-chan struct{}, retentionDays int) {
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				upload, download := collector.GetHourlyTotalsAndReset()
				if upload > 0 || download > 0 {
					if err := s.SaveHourlyNetwork(upload, download); err != nil {
						log.Println("保存每小时网络数据失败:", err)
					}
				}

				if err := s.SaveMonthlyNetwork(); err != nil {
					log.Println("保存月度网络汇总失败:", err)
				}

				if retentionDays > 0 {
					if err := s.CleanOldData(retentionDays); err != nil {
						log.Println("清理历史数据失败:", err)
					} else {
						log.Printf("已清理 %d 天前的历史数据", retentionDays)
					}
				}
			case <-stopCh:
				return
			}
		}
	}()
}

func (s *DB) GetMonthlyNetwork(startMonth, endMonth string, limit int) ([]MonthlyNetwork, error) {
	query := `SELECT id, year_month, upload, download, created_at FROM monthly_network WHERE year_month >= ? AND year_month <= ?`
	if limit > 0 {
		query += fmt.Sprintf(" ORDER BY year_month DESC LIMIT %d", limit)
	} else {
		query += " ORDER BY year_month DESC"
	}
	rows, err := s.db.Query(query, startMonth, endMonth)
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
	return result, nil
}

func (s *DB) CleanOldData(retentionDays int) error {
	sql := fmt.Sprintf("DELETE FROM daily_network WHERE date < date('now', '-%d days')", retentionDays)
	if _, err := s.db.Exec(sql); err != nil {
		return err
	}

	_, err := s.db.Exec("DELETE FROM monthly_network WHERE year_month < strftime('%Y-%m', 'now', '-12 months')")
	return err
}
