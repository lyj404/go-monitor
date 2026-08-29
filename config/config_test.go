package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: 70000\nmonitor:\n  interval: 3\nalert:\n  interval: 60\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("expected invalid config to fail validation")
	}
}

func TestReloadIsAtomicWhenSaveFails(t *testing.T) {
	t.Parallel()

	cfg := FromSnapshot(filepath.Join(t.TempDir(), "missing", "config.yaml"), Snapshot{
		Name: "before",
		Auth: AuthConfig{
			Username: "user",
			Password: "pass",
		},
		Monitor: MonitorConfig{Interval: 3},
		Alert:   AlertConfig{RetentionDays: 7},
	})

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

	cfg := FromSnapshot(path, Snapshot{
		Server:  ServerConfig{Port: 8080},
		Name:    "before",
		Monitor: MonitorConfig{Interval: 3},
		Alert:   AlertConfig{RetentionDays: 7, Interval: 60},
	})

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

func TestReloadRejectsInvalidUpdate(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: 8080\nmonitor:\n  interval: 3\nalert:\n  interval: 60\n"), 0644); err != nil {
		t.Fatalf("write seed config: %v", err)
	}

	cfg := FromSnapshot(path, Snapshot{
		Server:  ServerConfig{Port: 8080},
		Monitor: MonitorConfig{Interval: 3},
		Alert:   AlertConfig{Interval: 60},
	})

	updated := map[string]interface{}{
		"server": map[string]interface{}{
			"timezone": "Invalid/Timezone",
		},
	}

	if _, err := cfg.Reload(updated); err == nil {
		t.Fatal("expected invalid reload to fail")
	}
	if cfg.Snapshot().Monitor.Interval != 3 {
		t.Fatal("invalid reload should not mutate in-memory config")
	}
}

func TestDiskIOPSToggle(t *testing.T) {
	t.Parallel()

	// An absent key defaults to enabled so configs saved before the toggle
	// existed keep tracking IOPS.
	if !(MonitorConfig{}).IOPSEnabled() {
		t.Fatal("absent disk_iops should default to enabled")
	}
	off := false
	if (MonitorConfig{DiskIOPS: &off}).IOPSEnabled() {
		t.Fatal("explicit false should disable IOPS")
	}
	on := true
	if !(MonitorConfig{DiskIOPS: &on}).IOPSEnabled() {
		t.Fatal("explicit true should enable IOPS")
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("monitor:\n  interval: 3\n  disk_io: true\n"), 0644); err != nil {
		t.Fatalf("write seed config: %v", err)
	}
	cfg := FromSnapshot(path, Snapshot{
		Server:  ServerConfig{Port: 8080},
		Monitor: MonitorConfig{Interval: 3, DiskIO: true, DiskIOPS: &on},
		Alert:   AlertConfig{Interval: 60},
	})

	updated := map[string]interface{}{
		"monitor": map[string]interface{}{
			"disk_iops": false,
		},
	}
	if _, err := cfg.Reload(updated); err != nil {
		t.Fatalf("reload: %v", err)
	}
	snap := cfg.Snapshot()
	if snap.Monitor.IOPSEnabled() {
		t.Fatal("reload should disable IOPS monitoring")
	}
	// Toggling one key must not touch the other monitor fields.
	if !snap.Monitor.DiskIO {
		t.Fatal("unrelated monitor fields must survive reload")
	}
}

func TestUpdateCheckToggle(t *testing.T) {
	t.Parallel()

	// An absent key defaults to enabled so configs saved before the
	// version check existed keep the feature on.
	if !(UpdateCheckConfig{}).CheckEnabled() {
		t.Fatal("absent update_check.enabled should default to enabled")
	}
	off := false
	if (UpdateCheckConfig{Enabled: &off}).CheckEnabled() {
		t.Fatal("explicit false should disable the check")
	}
	on := true
	if !(UpdateCheckConfig{Enabled: &on}).CheckEnabled() {
		t.Fatal("explicit true should enable the check")
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("monitor:\n  interval: 3\n"), 0644); err != nil {
		t.Fatalf("write seed config: %v", err)
	}
	cfg := FromSnapshot(path, Snapshot{
		Server:  ServerConfig{Port: 8080},
		Monitor: MonitorConfig{Interval: 3},
		Alert:   AlertConfig{Interval: 60},
	})

	updated := map[string]interface{}{
		"update_check": map[string]interface{}{
			"enabled": false,
		},
	}
	if _, err := cfg.Reload(updated); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg.Snapshot().Update.CheckEnabled() {
		t.Fatal("reload should disable the update check")
	}

	// The disabled flag must be persisted to disk and survive a reload.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload from disk: %v", err)
	}
	_ = data
	if reloaded.Snapshot().Update.CheckEnabled() {
		t.Fatal("disabled state must persist to disk")
	}

	// MaskSensitive must expose the field or the settings page would
	// render an absent checkbox and save-enabled-by-omission.
	if _, ok := cfg.MaskSensitive()["update_check"]; !ok {
		t.Fatal("MaskSensitive must include update_check")
	}
}
