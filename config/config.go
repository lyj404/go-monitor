package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const maskPlaceholder = "****"

// Snapshot is an immutable, point-in-time copy of the configuration.
// It contains no synchronization primitives, so it is safe to copy by value
// and pass between goroutines.
type Snapshot struct {
	Name    string            `yaml:"name"`
	Server  ServerConfig      `yaml:"server"`
	Auth    AuthConfig        `yaml:"auth"`
	Monitor MonitorConfig     `yaml:"monitor"`
	SMTP    SMTPConfig        `yaml:"smtp"`
	Alert   AlertConfig       `yaml:"alert"`
	Update  UpdateCheckConfig `yaml:"update_check"`
}

// UpdateCheckConfig controls the manual version check against GitHub
// Releases. Enabled is a *bool so an absent key can default to enabled:
// config files saved before the version check existed do not contain it.
type UpdateCheckConfig struct {
	Enabled *bool `yaml:"enabled"`
}

// CheckEnabled reports whether the version check feature is on.
func (u UpdateCheckConfig) CheckEnabled() bool {
	return u.Enabled == nil || *u.Enabled
}

// Config is the mutable holder for the live configuration. It owns the
// synchronization and the on-disk path; the data lives in an inner Snapshot
// so callers never copy mutex state. Always use *Config — never copy by value.
type Config struct {
	cfgPath string

	mu     sync.RWMutex
	saveMu sync.Mutex

	data Snapshot
}

type ServerConfig struct {
	Port       int    `yaml:"port"`
	DataDir    string `yaml:"data_dir"`
	TrustProxy bool   `yaml:"trust_proxy"`
	Timezone   string `yaml:"timezone"`
}

type AuthConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type MonitorConfig struct {
	Interval    int  `yaml:"interval"`
	Memory      bool `yaml:"memory"`
	CPU         bool `yaml:"cpu"`
	NetworkUp   bool `yaml:"network_up"`
	NetworkDown bool `yaml:"network_down"`
	LanWanSplit bool `yaml:"lan_wan_split"`
	DiskRoot    bool `yaml:"disk_root"`
	DiskIO      bool `yaml:"disk_io"`
	// DiskIOPS is a *bool so an absent key can default to enabled: IOPS
	// used to be tracked whenever DiskIO was on, and older config files
	// saved by the settings page do not contain the key.
	DiskIOPS *bool `yaml:"disk_iops"`
	Process  bool  `yaml:"process"`
	Uptime   bool  `yaml:"uptime"`
	TCPStat  bool  `yaml:"tcpstat"`
	CPUTemp  bool  `yaml:"cpu_temp"`
}

// IOPSEnabled reports whether disk IOPS monitoring is on.
func (m MonitorConfig) IOPSEnabled() bool {
	return m.DiskIOPS == nil || *m.DiskIOPS
}

type SMTPConfig struct {
	Host string   `yaml:"host"`
	Port int      `yaml:"port"`
	User string   `yaml:"user"`
	Pass string   `yaml:"pass"`
	To   []string `yaml:"to"`
}

type AlertConfig struct {
	Enabled                bool    `yaml:"enabled"`
	Duration               int     `yaml:"duration"`
	Memory                 bool    `yaml:"memory"`
	MemoryThreshold        float64 `yaml:"memory_threshold"`
	CPU                    bool    `yaml:"cpu"`
	CPUThreshold           float64 `yaml:"cpu_threshold"`
	Disk                   bool    `yaml:"disk"`
	DiskThreshold          float64 `yaml:"disk_threshold"`
	NetworkUp              bool    `yaml:"network_up"`
	NetworkUpThreshold     int64   `yaml:"network_up_threshold"`
	NetworkDown            bool    `yaml:"network_down"`
	NetworkDownThreshold   int64   `yaml:"network_down_threshold"`
	DiskRead               bool    `yaml:"disk_read"`
	DiskReadThreshold      int64   `yaml:"disk_read_threshold"`
	DiskWrite              bool    `yaml:"disk_write"`
	DiskWriteThreshold     int64   `yaml:"disk_write_threshold"`
	DiskIOPS               bool    `yaml:"disk_iops"`
	DiskIOPSThreshold      int64   `yaml:"disk_iops_threshold"`
	Interval               int     `yaml:"interval"`
	RetentionDays          int     `yaml:"retention_days"`
	MonthlyRetentionMonths int     `yaml:"monthly_retention_months"`
	AlertRetentionDays     int     `yaml:"alert_retention_days"`
	MetricsRetentionDays   int     `yaml:"metrics_retention_days"`
	Process                bool    `yaml:"process"`
	ProcessThreshold       int     `yaml:"process_threshold"`
	CPUTemp                bool    `yaml:"cpu_temp"`
	CPUTempThreshold       float64 `yaml:"cpu_temp_threshold"`
	CloseWait              bool    `yaml:"close_wait"`
	CloseWaitThreshold     int     `yaml:"close_wait_threshold"`
}

// FromSnapshot builds a Config from a value-typed Snapshot. Intended for
// tests that need to construct a Config without going through Load().
func FromSnapshot(path string, snap Snapshot) *Config {
	return &Config{cfgPath: path, data: cloneSnapshot(snap)}
}

func (c *Config) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneSnapshot(c.data)
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var snap Snapshot
	if err := yaml.Unmarshal(data, &snap); err != nil {
		return nil, err
	}

	if snap.Server.Port == 0 {
		snap.Server.Port = 8080
	}

	if snap.Monitor.Interval == 0 {
		snap.Monitor.Interval = 3
	}

	if snap.Alert.Interval == 0 {
		snap.Alert.Interval = 300
	}

	if snap.Alert.RetentionDays == 0 {
		snap.Alert.RetentionDays = 30
	}

	if snap.Alert.MonthlyRetentionMonths == 0 {
		snap.Alert.MonthlyRetentionMonths = 12
	}

	if snap.Alert.AlertRetentionDays == 0 {
		snap.Alert.AlertRetentionDays = snap.Alert.RetentionDays
	}

	if snap.Alert.MetricsRetentionDays == 0 {
		snap.Alert.MetricsRetentionDays = snap.Alert.RetentionDays
	}

	// Optional collectors: disabled by default, enable via config
	// loadavg is removed - not useful for most users

	if err := validate(&snap); err != nil {
		return nil, err
	}

	return &Config{cfgPath: path, data: snap}, nil
}

// MaskSensitive returns a copy with password fields replaced by ****
func (c *Config) MaskSensitive() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return map[string]interface{}{
		"name": c.data.Name,
		"server": map[string]interface{}{
			"port":        c.data.Server.Port,
			"data_dir":    c.data.Server.DataDir,
			"trust_proxy": c.data.Server.TrustProxy,
			"timezone":    c.data.Server.Timezone,
		},
		"auth": map[string]interface{}{
			"username": c.data.Auth.Username,
			"password": maskPlaceholder,
		},
		"monitor": map[string]interface{}{
			"interval":      c.data.Monitor.Interval,
			"memory":        c.data.Monitor.Memory,
			"cpu":           c.data.Monitor.CPU,
			"network_up":    c.data.Monitor.NetworkUp,
			"network_down":  c.data.Monitor.NetworkDown,
			"lan_wan_split": c.data.Monitor.LanWanSplit,
			"disk_root":     c.data.Monitor.DiskRoot,
			"disk_io":       c.data.Monitor.DiskIO,
			"disk_iops":     c.data.Monitor.IOPSEnabled(),
			"process":       c.data.Monitor.Process,
			"uptime":        c.data.Monitor.Uptime,
			"tcpstat":       c.data.Monitor.TCPStat,
			"cpu_temp":      c.data.Monitor.CPUTemp,
		},
		"smtp": map[string]interface{}{
			"host": c.data.SMTP.Host,
			"port": c.data.SMTP.Port,
			"user": c.data.SMTP.User,
			"pass": maskPlaceholder,
			"to":   c.data.SMTP.To,
		},
		"alert": map[string]interface{}{
			"enabled":                  c.data.Alert.Enabled,
			"memory":                   c.data.Alert.Memory,
			"memory_threshold":         c.data.Alert.MemoryThreshold,
			"cpu":                      c.data.Alert.CPU,
			"cpu_threshold":            c.data.Alert.CPUThreshold,
			"disk":                     c.data.Alert.Disk,
			"disk_threshold":           c.data.Alert.DiskThreshold,
			"network_up":               c.data.Alert.NetworkUp,
			"network_up_threshold":     c.data.Alert.NetworkUpThreshold,
			"network_down":             c.data.Alert.NetworkDown,
			"network_down_threshold":   c.data.Alert.NetworkDownThreshold,
			"disk_read":                c.data.Alert.DiskRead,
			"disk_read_threshold":      c.data.Alert.DiskReadThreshold,
			"disk_write":               c.data.Alert.DiskWrite,
			"disk_write_threshold":     c.data.Alert.DiskWriteThreshold,
			"disk_iops":                c.data.Alert.DiskIOPS,
			"disk_iops_threshold":      c.data.Alert.DiskIOPSThreshold,
			"interval":                 c.data.Alert.Interval,
			"duration":                 c.data.Alert.Duration,
			"retention_days":           c.data.Alert.RetentionDays,
			"monthly_retention_months": c.data.Alert.MonthlyRetentionMonths,
			"alert_retention_days":     c.data.Alert.AlertRetentionDays,
			"metrics_retention_days":   c.data.Alert.MetricsRetentionDays,
			"process":                  c.data.Alert.Process,
			"process_threshold":        c.data.Alert.ProcessThreshold,
			"cpu_temp":                 c.data.Alert.CPUTemp,
			"cpu_temp_threshold":       c.data.Alert.CPUTempThreshold,
			"close_wait":               c.data.Alert.CloseWait,
			"close_wait_threshold":     c.data.Alert.CloseWaitThreshold,
		},
		"update_check": map[string]interface{}{
			"enabled": c.data.Update.CheckEnabled(),
		},
	}
}

// Reload merges updated config, preserving sensitive fields when masked.
// Returns true if Monitor.Interval changed. The on-disk write happens
// outside the in-memory lock so concurrent Snapshot() calls are not
// blocked on disk I/O. saveMu serializes concurrent Reload calls so
// the file write and in-memory swap stay coherent.
func (c *Config) Reload(updated map[string]interface{}) (bool, error) {
	c.saveMu.Lock()
	defer c.saveMu.Unlock()

	c.mu.RLock()
	oldInterval := c.data.Monitor.Interval
	updatedSnap := cloneSnapshot(c.data)
	cfgPath := c.cfgPath
	c.mu.RUnlock()

	applyUpdates(&updatedSnap, updated)
	if err := validate(&updatedSnap); err != nil {
		return false, err
	}

	log.Println("保存配置到文件:", cfgPath)
	if err := saveConfig(cfgPath, updatedSnap); err != nil {
		log.Println("保存文件失败:", err)
		return false, err
	}

	c.mu.Lock()
	c.data = updatedSnap
	intervalChanged := c.data.Monitor.Interval != oldInterval
	c.mu.Unlock()

	log.Println("配置保存成功")
	return intervalChanged, nil
}

func cloneSnapshot(s Snapshot) Snapshot {
	out := s
	if s.SMTP.To != nil {
		out.SMTP.To = append([]string{}, s.SMTP.To...)
	}
	if s.Monitor.DiskIOPS != nil {
		v := *s.Monitor.DiskIOPS
		out.Monitor.DiskIOPS = &v
	}
	if s.Update.Enabled != nil {
		v := *s.Update.Enabled
		out.Update.Enabled = &v
	}
	return out
}

func applyUpdates(c *Snapshot, updated map[string]interface{}) {
	if name, ok := updated["name"].(string); ok {
		c.Name = name
	}

	if srv, ok := updated["server"].(map[string]interface{}); ok {
		if v, ok := srv["port"].(float64); ok {
			c.Server.Port = int(v)
		}
		if v, ok := srv["data_dir"].(string); ok {
			c.Server.DataDir = v
		}
		if v, ok := srv["trust_proxy"].(bool); ok {
			c.Server.TrustProxy = v
		}
		if v, ok := srv["timezone"].(string); ok {
			c.Server.Timezone = v
		}
	}

	if auth, ok := updated["auth"].(map[string]interface{}); ok {
		if v, ok := auth["username"].(string); ok {
			c.Auth.Username = v
		}
		if v, ok := auth["password"].(string); ok && v != maskPlaceholder && v != "" {
			c.Auth.Password = v
		}
	}

	if mon, ok := updated["monitor"].(map[string]interface{}); ok {
		if v, ok := mon["interval"].(float64); ok && v > 0 {
			c.Monitor.Interval = int(v)
		}
		if v, ok := mon["memory"].(bool); ok {
			c.Monitor.Memory = v
		}
		if v, ok := mon["cpu"].(bool); ok {
			c.Monitor.CPU = v
		}
		if v, ok := mon["network_up"].(bool); ok {
			c.Monitor.NetworkUp = v
		}
		if v, ok := mon["network_down"].(bool); ok {
			c.Monitor.NetworkDown = v
		}
		if v, ok := mon["lan_wan_split"].(bool); ok {
			c.Monitor.LanWanSplit = v
		}
		if v, ok := mon["disk_root"].(bool); ok {
			c.Monitor.DiskRoot = v
		}
		if v, ok := mon["disk_io"].(bool); ok {
			c.Monitor.DiskIO = v
		}
		if v, ok := mon["disk_iops"].(bool); ok {
			c.Monitor.DiskIOPS = &v
		}
		if v, ok := mon["process"].(bool); ok {
			c.Monitor.Process = v
		}
		if v, ok := mon["uptime"].(bool); ok {
			c.Monitor.Uptime = v
		}
		if v, ok := mon["tcpstat"].(bool); ok {
			c.Monitor.TCPStat = v
		}
		if v, ok := mon["cpu_temp"].(bool); ok {
			c.Monitor.CPUTemp = v
		}
	}

	if smtp, ok := updated["smtp"].(map[string]interface{}); ok {
		if v, ok := smtp["host"].(string); ok {
			c.SMTP.Host = v
		}
		if v, ok := smtp["port"].(float64); ok {
			c.SMTP.Port = int(v)
		}
		if v, ok := smtp["user"].(string); ok {
			c.SMTP.User = v
		}
		if v, ok := smtp["pass"].(string); ok && v != maskPlaceholder && v != "" {
			c.SMTP.Pass = v
		}
		if toSlice, ok := smtp["to"].([]interface{}); ok {
			// Respect an explicitly-provided list, including an empty one
			// (clears recipients). An absent "to" key leaves To unchanged.
			to := make([]string, 0, len(toSlice))
			for _, item := range toSlice {
				if s, ok := item.(string); ok {
					to = append(to, s)
				}
			}
			c.SMTP.To = to
		}
	}

	if alert, ok := updated["alert"].(map[string]interface{}); ok {
		if v, ok := alert["enabled"].(bool); ok {
			c.Alert.Enabled = v
		}
		if v, ok := alert["memory"].(bool); ok {
			c.Alert.Memory = v
		}
		if v, ok := alert["memory_threshold"].(float64); ok {
			c.Alert.MemoryThreshold = v
		}
		if v, ok := alert["cpu"].(bool); ok {
			c.Alert.CPU = v
		}
		if v, ok := alert["cpu_threshold"].(float64); ok {
			c.Alert.CPUThreshold = v
		}
		if v, ok := alert["disk"].(bool); ok {
			c.Alert.Disk = v
		}
		if v, ok := alert["disk_threshold"].(float64); ok {
			c.Alert.DiskThreshold = v
		}
		if v, ok := alert["network_up"].(bool); ok {
			c.Alert.NetworkUp = v
		}
		if v, ok := alert["network_up_threshold"].(float64); ok {
			c.Alert.NetworkUpThreshold = int64(v)
		}
		if v, ok := alert["network_down"].(bool); ok {
			c.Alert.NetworkDown = v
		}
		if v, ok := alert["network_down_threshold"].(float64); ok {
			c.Alert.NetworkDownThreshold = int64(v)
		}
		if v, ok := alert["disk_read"].(bool); ok {
			c.Alert.DiskRead = v
		}
		if v, ok := alert["disk_read_threshold"].(float64); ok {
			c.Alert.DiskReadThreshold = int64(v)
		}
		if v, ok := alert["disk_write"].(bool); ok {
			c.Alert.DiskWrite = v
		}
		if v, ok := alert["disk_write_threshold"].(float64); ok {
			c.Alert.DiskWriteThreshold = int64(v)
		}
		if v, ok := alert["disk_iops"].(bool); ok {
			c.Alert.DiskIOPS = v
		}
		if v, ok := alert["disk_iops_threshold"].(float64); ok {
			c.Alert.DiskIOPSThreshold = int64(v)
		}
		if v, ok := alert["interval"].(float64); ok && v > 0 {
			c.Alert.Interval = int(v)
		}
		if v, ok := alert["duration"].(float64); ok && v >= 0 {
			c.Alert.Duration = int(v)
		}
		if v, ok := alert["retention_days"].(float64); ok && v > 0 {
			c.Alert.RetentionDays = int(v)
		}
		if v, ok := alert["monthly_retention_months"].(float64); ok && v > 0 {
			c.Alert.MonthlyRetentionMonths = int(v)
		}
		if v, ok := alert["alert_retention_days"].(float64); ok && v > 0 {
			c.Alert.AlertRetentionDays = int(v)
		}
		if v, ok := alert["metrics_retention_days"].(float64); ok && v > 0 {
			c.Alert.MetricsRetentionDays = int(v)
		}
		if v, ok := alert["process"].(bool); ok {
			c.Alert.Process = v
		}
		if v, ok := alert["process_threshold"].(float64); ok {
			c.Alert.ProcessThreshold = int(v)
		}
		if v, ok := alert["cpu_temp"].(bool); ok {
			c.Alert.CPUTemp = v
		}
		if v, ok := alert["cpu_temp_threshold"].(float64); ok {
			c.Alert.CPUTempThreshold = v
		}
		if v, ok := alert["close_wait"].(bool); ok {
			c.Alert.CloseWait = v
		}
		if v, ok := alert["close_wait_threshold"].(float64); ok {
			c.Alert.CloseWaitThreshold = int(v)
		}
	}

	if upd, ok := updated["update_check"].(map[string]interface{}); ok {
		if v, ok := upd["enabled"].(bool); ok {
			c.Update.Enabled = &v
		}
	}
}

func saveConfig(path string, snap Snapshot) error {
	data, err := yaml.Marshal(snap)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmpPath, path)
}

func validate(c *Snapshot) error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("invalid server.port: %d", c.Server.Port)
	}
	if c.Monitor.Interval <= 0 {
		return fmt.Errorf("invalid monitor.interval: %d", c.Monitor.Interval)
	}
	if c.Alert.Interval <= 0 {
		return fmt.Errorf("invalid alert.interval: %d", c.Alert.Interval)
	}
	if c.Alert.Duration < 0 {
		return fmt.Errorf("invalid alert.duration: %d", c.Alert.Duration)
	}
	if c.Alert.RetentionDays < 0 {
		return fmt.Errorf("invalid alert.retention_days: %d", c.Alert.RetentionDays)
	}
	if c.Alert.MonthlyRetentionMonths < 0 {
		return fmt.Errorf("invalid alert.monthly_retention_months: %d", c.Alert.MonthlyRetentionMonths)
	}
	if c.Alert.AlertRetentionDays < 0 {
		return fmt.Errorf("invalid alert.alert_retention_days: %d", c.Alert.AlertRetentionDays)
	}
	if c.Alert.MetricsRetentionDays < 0 {
		return fmt.Errorf("invalid alert.metrics_retention_days: %d", c.Alert.MetricsRetentionDays)
	}
	if c.Server.Timezone != "" {
		if _, err := time.LoadLocation(c.Server.Timezone); err != nil {
			return fmt.Errorf("invalid server.timezone: %q", c.Server.Timezone)
		}
	}
	return nil
}
