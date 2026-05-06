package alerter

import (
	"encoding/base64"
	"fmt"
	"log"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"go-monitor/collector"
	"go-monitor/config"
)

type Alerter struct {
	conditionStartTimes map[string]time.Time
	lastSent            map[string]time.Time
	mu                  sync.Mutex
}

func New() *Alerter {
	return &Alerter{
		conditionStartTimes: make(map[string]time.Time),
		lastSent:            make(map[string]time.Time),
	}
}

func (a *Alerter) CheckWithConfig(m collector.Metrics, cfg config.Config) {
	if !cfg.Alert.Enabled {
		return
	}
	duration := time.Duration(cfg.Alert.Duration) * time.Second

	if cfg.Alert.CPU && m.CPU != nil {
		conditionMet := m.CPU.Usage >= cfg.Alert.CPUThreshold
		a.checkCondition("cpu", conditionMet, duration, func() {
			a.send("CPU", fmt.Sprintf("CPU使用率 %.1f%% 超过阈值 %.1f%%", m.CPU.Usage, cfg.Alert.CPUThreshold), cfg)
		})
	}

	if cfg.Alert.Memory && m.Memory != nil {
		conditionMet := m.Memory.Usage >= cfg.Alert.MemoryThreshold
		a.checkCondition("memory", conditionMet, duration, func() {
			a.send("内存", fmt.Sprintf("内存使用率 %.1f%% 超过阈值 %.1f%%", m.Memory.Usage, cfg.Alert.MemoryThreshold), cfg)
		})
	}

	if cfg.Alert.Disk && m.Disk != nil {
		conditionMet := m.Disk.Usage >= cfg.Alert.DiskThreshold
		a.checkCondition("disk", conditionMet, duration, func() {
			a.send("磁盘", fmt.Sprintf("磁盘使用率 %.1f%% 超过阈值 %.1f%%", m.Disk.Usage, cfg.Alert.DiskThreshold), cfg)
		})
	}

	if m.Network != nil {
		if cfg.Alert.NetworkUp {
			conditionMet := m.Network.Upload >= cfg.Alert.NetworkUpThreshold
			a.checkCondition("upload", conditionMet, duration, func() {
				a.send("网络上传", fmt.Sprintf("上传速率 %s 超过阈值 %s", formatBytes(m.Network.Upload), formatBytes(cfg.Alert.NetworkUpThreshold)), cfg)
			})
		}
		if cfg.Alert.NetworkDown {
			conditionMet := m.Network.Download >= cfg.Alert.NetworkDownThreshold
			a.checkCondition("download", conditionMet, duration, func() {
				a.send("网络下载", fmt.Sprintf("下载速率 %s 超过阈值 %s", formatBytes(m.Network.Download), formatBytes(cfg.Alert.NetworkDownThreshold)), cfg)
			})
		}
	}

	if m.DiskIO != nil {
		if cfg.Alert.DiskRead {
			conditionMet := m.DiskIO.ReadBytes >= cfg.Alert.DiskReadThreshold
			a.checkCondition("disk_read", conditionMet, duration, func() {
				a.send("磁盘读取", fmt.Sprintf("读取速率 %s 超过阈值 %s", formatBytes(m.DiskIO.ReadBytes), formatBytes(cfg.Alert.DiskReadThreshold)), cfg)
			})
		}
		if cfg.Alert.DiskWrite {
			conditionMet := m.DiskIO.WriteBytes >= cfg.Alert.DiskWriteThreshold
			a.checkCondition("disk_write", conditionMet, duration, func() {
				a.send("磁盘写入", fmt.Sprintf("写入速率 %s 超过阈值 %s", formatBytes(m.DiskIO.WriteBytes), formatBytes(cfg.Alert.DiskWriteThreshold)), cfg)
			})
		}
	}
}

func (a *Alerter) checkCondition(name string, conditionMet bool, duration time.Duration, alertFunc func()) {
	a.mu.Lock()
	defer a.mu.Unlock()

	startTime, exists := a.conditionStartTimes[name]

	if conditionMet {
		if !exists {
			a.conditionStartTimes[name] = time.Now()
			return
		}
		if time.Since(startTime) >= duration {
			alertFunc()
			delete(a.conditionStartTimes, name)
		}
	} else {
		delete(a.conditionStartTimes, name)
	}
}

func (a *Alerter) send(subject, body string, cfg config.Config) {
	interval := time.Duration(cfg.Alert.Interval) * time.Second
	if lastSent, ok := a.lastSent[subject]; ok && time.Since(lastSent) < interval {
		return
	}
	a.lastSent[subject] = time.Now()

	smtpCfg := cfg.SMTP
	addr := fmt.Sprintf("%s:%d", smtpCfg.Host, smtpCfg.Port)
	toList := strings.Join(smtpCfg.To, ", ")

	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600) // 兜底，防止时区加载失败
	}
	now := time.Now()
	serverTime := now.Format("2006-01-02 15:04:05 MST")
	beijingTime := now.In(loc).Format("2006-01-02 15:04:05 MST")
	emailBody := fmt.Sprintf(
		"服务器: %s\n%s\n服务器时间: %s\n北京时间: %s",
		cfg.Name,
		body,
		serverTime,
		beijingTime,
	)
	encodedSubject := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("[监控报警][%s] %s", cfg.Name, subject)))
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: =?UTF-8?B?%s?=\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		smtpCfg.User, toList, encodedSubject, emailBody)

	go func() {
		auth := smtp.PlainAuth("", smtpCfg.User, smtpCfg.Pass, smtpCfg.Host)
		err := smtp.SendMail(addr, auth, smtpCfg.User, smtpCfg.To, []byte(msg))
		if err != nil {
			log.Printf("报警邮件发送失败 [%s]: %v", subject, err)
		} else {
			log.Printf("报警邮件已发送 [%s]: %s", subject, body)
		}
	}()
}

func formatBytes(b int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
		TB = GB * 1024
	)
	switch {
	case b >= TB:
		return fmt.Sprintf("%.2f TB", float64(b)/float64(TB))
	case b >= GB:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.2f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.2f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
