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

type Config struct {
	cfgPath string `yaml:"-"`

	mu     sync.RWMutex `yaml:"-"`
	saveMu sync.Mutex   `yaml:"-"`

	Name    string        `yaml:"name"`
	Server  ServerConfig  `yaml:"server"`
	Auth    AuthConfig    `yaml:"auth"`
	Monitor MonitorConfig `yaml:"monitor"`
	SMTP    SMTPConfig    `yaml:"smtp"`
	Alert   AlertConfig   `yaml:"alert"`
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
	DiskRoot    bool `yaml:"disk_root"`
	DiskIO      bool `yaml:"disk_io"`
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
	Interval               int     `yaml:"interval"`
	RetentionDays          int     `yaml:"retention_days"`
	MonthlyRetentionMonths int     `yaml:"monthly_retention_months"`
}

type fileConfig struct {
	Name    string        `yaml:"name"`
	Server  ServerConfig  `yaml:"server"`
	Auth    AuthConfig    `yaml:"auth"`
	Monitor MonitorConfig `yaml:"monitor"`
	SMTP    SMTPConfig    `yaml:"smtp"`
	Alert   AlertConfig   `yaml:"alert"`
}

func (c *Config) Snapshot() Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cloneLocked()
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	cfg.cfgPath = path

	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}

	if cfg.Monitor.Interval == 0 {
		cfg.Monitor.Interval = 3
	}

	if cfg.Alert.Interval == 0 {
		cfg.Alert.Interval = 300
	}

	if cfg.Alert.RetentionDays == 0 {
		cfg.Alert.RetentionDays = 30
	}

	if cfg.Alert.MonthlyRetentionMonths == 0 {
		cfg.Alert.MonthlyRetentionMonths = 12
	}

	if err := validate(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// MaskSensitive returns a copy with password fields replaced by ****
func (c *Config) MaskSensitive() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return map[string]interface{}{
		"name": c.Name,
		"server": map[string]interface{}{
			"port":        c.Server.Port,
			"data_dir":    c.Server.DataDir,
			"trust_proxy": c.Server.TrustProxy,
			"timezone":    c.Server.Timezone,
		},
		"auth": map[string]interface{}{
			"username": c.Auth.Username,
			"password": maskPlaceholder,
		},
		"monitor": map[string]interface{}{
			"interval":     c.Monitor.Interval,
			"memory":       c.Monitor.Memory,
			"cpu":          c.Monitor.CPU,
			"network_up":   c.Monitor.NetworkUp,
			"network_down": c.Monitor.NetworkDown,
			"disk_root":    c.Monitor.DiskRoot,
			"disk_io":      c.Monitor.DiskIO,
		},
		"smtp": map[string]interface{}{
			"host": c.SMTP.Host,
			"port": c.SMTP.Port,
			"user": c.SMTP.User,
			"pass": maskPlaceholder,
			"to":   c.SMTP.To,
		},
		"alert": map[string]interface{}{
			"enabled":                  c.Alert.Enabled,
			"memory":                   c.Alert.Memory,
			"memory_threshold":         c.Alert.MemoryThreshold,
			"cpu":                      c.Alert.CPU,
			"cpu_threshold":            c.Alert.CPUThreshold,
			"disk":                     c.Alert.Disk,
			"disk_threshold":           c.Alert.DiskThreshold,
			"network_up":               c.Alert.NetworkUp,
			"network_up_threshold":     c.Alert.NetworkUpThreshold,
			"network_down":             c.Alert.NetworkDown,
			"network_down_threshold":   c.Alert.NetworkDownThreshold,
			"disk_read":                c.Alert.DiskRead,
			"disk_read_threshold":      c.Alert.DiskReadThreshold,
			"disk_write":               c.Alert.DiskWrite,
			"disk_write_threshold":     c.Alert.DiskWriteThreshold,
			"interval":                 c.Alert.Interval,
			"duration":                 c.Alert.Duration,
			"retention_days":           c.Alert.RetentionDays,
			"monthly_retention_months": c.Alert.MonthlyRetentionMonths,
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
	oldInterval := c.Monitor.Interval
	updatedCfg := c.cloneLocked()
	c.mu.RUnlock()

	applyUpdates(&updatedCfg, updated)
	if err := validate(&updatedCfg); err != nil {
		return false, err
	}

	log.Println("保存配置到文件:", updatedCfg.cfgPath)
	if err := saveConfig(updatedCfg.cfgPath, updatedCfg); err != nil {
		log.Println("保存文件失败:", err)
		return false, err
	}

	c.mu.Lock()
	c.applyLocked(updatedCfg)
	intervalChanged := c.Monitor.Interval != oldInterval
	c.mu.Unlock()

	log.Println("配置保存成功")
	return intervalChanged, nil
}

func (c *Config) cloneLocked() Config {
	return Config{
		cfgPath: c.cfgPath,
		Name:    c.Name,
		Server:  c.Server,
		Auth:    c.Auth,
		Monitor: c.Monitor,
		SMTP: SMTPConfig{
			Host: c.SMTP.Host,
			Port: c.SMTP.Port,
			User: c.SMTP.User,
			Pass: c.SMTP.Pass,
			To:   append([]string{}, c.SMTP.To...),
		},
		Alert: c.Alert,
	}
}

func (c *Config) applyLocked(updated Config) {
	c.cfgPath = updated.cfgPath
	c.Name = updated.Name
	c.Server = updated.Server
	c.Auth = updated.Auth
	c.Monitor = updated.Monitor
	c.SMTP = SMTPConfig{
		Host: updated.SMTP.Host,
		Port: updated.SMTP.Port,
		User: updated.SMTP.User,
		Pass: updated.SMTP.Pass,
		To:   append([]string{}, updated.SMTP.To...),
	}
	c.Alert = updated.Alert
}

func applyUpdates(c *Config, updated map[string]interface{}) {
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
		if v, ok := mon["disk_root"].(bool); ok {
			c.Monitor.DiskRoot = v
		}
		if v, ok := mon["disk_io"].(bool); ok {
			c.Monitor.DiskIO = v
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
			var to []string
			for _, item := range toSlice {
				if s, ok := item.(string); ok {
					to = append(to, s)
				}
			}
			if len(to) > 0 {
				c.SMTP.To = to
			}
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
	}
}

func saveConfig(path string, cfg Config) error {
	data, err := yaml.Marshal(fileConfig{
		Name:    cfg.Name,
		Server:  cfg.Server,
		Auth:    cfg.Auth,
		Monitor: cfg.Monitor,
		SMTP:    cfg.SMTP,
		Alert:   cfg.Alert,
	})
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

func validate(c *Config) error {
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
	if c.Server.Timezone != "" {
		if _, err := time.LoadLocation(c.Server.Timezone); err != nil {
			return fmt.Errorf("invalid server.timezone: %q", c.Server.Timezone)
		}
	}
	return nil
}
