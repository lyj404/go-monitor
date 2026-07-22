package store

import (
	"path/filepath"
	"testing"
	"time"
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
