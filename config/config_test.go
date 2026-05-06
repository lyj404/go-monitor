package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReloadIsAtomicWhenSaveFails(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		cfgPath: filepath.Join(t.TempDir(), "missing", "config.yaml"),
		Name:    "before",
		Auth: AuthConfig{
			Username: "user",
			Password: "pass",
		},
		Monitor: MonitorConfig{Interval: 3},
		Alert:   AlertConfig{RetentionDays: 7},
	}

	updated := map[string]interface{}{
		"name": "after",
		"monitor": map[string]interface{}{
			"interval": float64(10),
		},
	}

	intervalChanged, err := cfg.Reload(updated)
	if err == nil {
		t.Fatal("expected save failure")
	}
	if intervalChanged {
		t.Fatal("interval should not report changed when save fails")
	}

	snapshot := cfg.Snapshot()
	if snapshot.Name != "before" {
		t.Fatalf("name changed unexpectedly: %q", snapshot.Name)
	}
	if snapshot.Monitor.Interval != 3 {
		t.Fatalf("interval changed unexpectedly: %d", snapshot.Monitor.Interval)
	}
}

func TestReloadPersistsOnlyAfterSuccessfulSave(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("name: before\nmonitor:\n  interval: 3\n"), 0644); err != nil {
		t.Fatalf("write seed config: %v", err)
	}

	cfg := &Config{
		cfgPath: path,
		Name:    "before",
		Monitor: MonitorConfig{Interval: 3},
		Alert:   AlertConfig{RetentionDays: 7},
	}

	updated := map[string]interface{}{
		"name": "after",
		"monitor": map[string]interface{}{
			"interval": float64(5),
		},
	}

	intervalChanged, err := cfg.Reload(updated)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if !intervalChanged {
		t.Fatal("expected intervalChanged to be true")
	}

	snapshot := cfg.Snapshot()
	if snapshot.Name != "after" {
		t.Fatalf("unexpected name: %q", snapshot.Name)
	}
	if snapshot.Monitor.Interval != 5 {
		t.Fatalf("unexpected interval: %d", snapshot.Monitor.Interval)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if string(data) == "" {
		t.Fatal("saved config is empty")
	}
}
