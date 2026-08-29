package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lyj404/go-monitor/collector"
)

func TestNextHour(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 10, 23, 45, 0, time.UTC)
	want := time.Date(2026, 5, 6, 11, 0, 0, 0, time.UTC)
	if got := nextHour(now); !got.Equal(want) {
		t.Fatalf("nextHour(%v) = %v, want %v", now, got, want)
	}

	loc := time.FixedZone("CST", 8*3600)
	nowCST := time.Date(2026, 5, 6, 10, 23, 45, 0, loc)
	wantCST := time.Date(2026, 5, 6, 11, 0, 0, 0, loc)
	if got := nextHour(nowCST); !got.Equal(wantCST) {
		t.Fatalf("nextHour(%v) = %v, want %v", nowCST, got, wantCST)
	}
}

func TestSaveHourlyNetworkPersistsZeroTrafficDay(t *testing.T) {
	t.Parallel()

	db, err := NewDB(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new db: %v", err)
	}
	defer db.Close()

	if err := db.SaveHourlyNetwork(0, 0, 0, 0, 0, 0, time.UTC); err != nil {
		t.Fatalf("save hourly zero traffic: %v", err)
	}

	today := time.Now().UTC().Format("2006-01-02")
	rows, err := db.GetDailyNetwork(today, today, 10)
	if err != nil {
		t.Fatalf("get daily network: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Upload != 0 || rows[0].Download != 0 {
		t.Fatalf("expected zero totals, got upload=%d download=%d", rows[0].Upload, rows[0].Download)
	}
	if rows[0].LanUpload != 0 || rows[0].LanDownload != 0 || rows[0].WanUpload != 0 || rows[0].WanDownload != 0 {
		t.Fatalf("expected zero lan/wan, got lan_up=%d lan_down=%d wan_up=%d wan_down=%d", rows[0].LanUpload, rows[0].LanDownload, rows[0].WanUpload, rows[0].WanDownload)
	}
}

func TestSaveMonthlyNetworkPersistsZeroTrafficMonth(t *testing.T) {
	t.Parallel()

	db, err := NewDB(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new db: %v", err)
	}
	defer db.Close()

	if err := db.SaveHourlyNetwork(0, 0, 0, 0, 0, 0, time.UTC); err != nil {
		t.Fatalf("seed daily zero traffic: %v", err)
	}
	if err := db.SaveMonthlyNetwork(time.UTC); err != nil {
		t.Fatalf("save monthly zero traffic: %v", err)
	}

	month := time.Now().UTC().Format("2006-01")
	rows, err := db.GetMonthlyNetwork(month, month, 10)
	if err != nil {
		t.Fatalf("get monthly network: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Upload != 0 || rows[0].Download != 0 {
		t.Fatalf("expected zero totals, got upload=%d download=%d", rows[0].Upload, rows[0].Download)
	}
	if rows[0].LanUpload != 0 || rows[0].LanDownload != 0 || rows[0].WanUpload != 0 || rows[0].WanDownload != 0 {
		t.Fatalf("expected zero lan/wan, got lan_up=%d lan_down=%d wan_up=%d wan_down=%d", rows[0].LanUpload, rows[0].LanDownload, rows[0].WanUpload, rows[0].WanDownload)
	}
}

func TestCleanOldDataKeepsCurrentMonth(t *testing.T) {
	t.Parallel()

	db, err := NewDB(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new db: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	retention := 7
	cutoff := now.AddDate(0, 0, -retention).Format("2006-01-02")
	dates := map[string]bool{
		now.AddDate(0, 0, -40).Format("2006-01-02"): false, // older than retention → deleted
		now.AddDate(0, 0, -10).Format("2006-01-02"): false, // older than retention, kept if in current month
		now.AddDate(0, 0, -2).Format("2006-01-02"):  false, // within retention → kept
	}
	monthStart := now.Format("2006-01") + "-01"
	for date := range dates {
		if _, err := db.db.Exec(
			`INSERT INTO daily_network (date, upload, download, created_at) VALUES (?, ?, 0, 0)`,
			date, 100,
		); err != nil {
			t.Fatalf("seed row %s: %v", date, err)
		}
	}

	if err := db.CleanOldData(retention, 0, time.UTC); err != nil {
		t.Fatalf("clean old data: %v", err)
	}

	for date := range dates {
		wantKept := date >= cutoff || date >= monthStart
		var n int
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM daily_network WHERE date = ?`, date).Scan(&n); err != nil {
			t.Fatalf("query row %s: %v", date, err)
		}
		if wantKept && n != 1 {
			t.Fatalf("expected current-month row %s to be kept (monthly totals are re-aggregated from daily rows)", date)
		}
		if !wantKept && n != 0 {
			t.Fatalf("expected pre-month row %s to be deleted", date)
		}
	}
}

func TestSaveDailyMetricsSampleCountCatchesUp(t *testing.T) {
	t.Parallel()

	db, err := NewDB(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new db: %v", err)
	}
	defer db.Close()

	first := collector.HourlyMetrics{AvgCPU: 10, MaxCPU: 20, CPUSamples: 2, AvgMemory: 30, MaxMemory: 40, MemSamples: 3, AvgDisk: 50, MaxDisk: 60, DiskSamples: 1, SampleCount: 3}
	if err := db.SaveDailyMetrics(first, time.UTC); err != nil {
		t.Fatalf("save first hourly metrics: %v", err)
	}

	second := collector.HourlyMetrics{AvgCPU: 15, MaxCPU: 25, CPUSamples: 5, AvgMemory: 35, MaxMemory: 45, MemSamples: 1, DiskSamples: 0, SampleCount: 5}
	if err := db.SaveDailyMetrics(second, time.UTC); err != nil {
		t.Fatalf("save second hourly metrics: %v", err)
	}

	today := time.Now().UTC().Format("2006-01-02")
	rows, err := db.GetDailyMetrics(today, today, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("get daily metrics: rows=%d err=%v", len(rows), err)
	}

	// sample_count must reflect the per-type columns updated in the same
	// statement, not their pre-update values.
	wantCount := 2 + 5 // max(cpu 2+5, mem 3+1, disk 1+0)
	if rows[0].SampleCount != wantCount {
		t.Fatalf("sample_count = %d, want %d (must not lag one hour behind)", rows[0].SampleCount, wantCount)
	}

	// CPU average is re-weighted by the per-type sample counts.
	wantAvgCPU := float64(10*2+15*5) / 7
	if diff := rows[0].AvgCPU - wantAvgCPU; diff > 0.01 || diff < -0.01 {
		t.Fatalf("avg_cpu = %v, want %v", rows[0].AvgCPU, wantAvgCPU)
	}
}
