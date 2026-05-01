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
	cfg                 *config.Config
	conditionStartTimes map[string]time.Time
	lastSent            map[string]time.Time
	mu                  sync.Mutex
}

func New(cfg *config.Config) *Alerter {
	return &Alerter{
		cfg:                 cfg,
		conditionStartTimes: make(map[string]time.Time),
		lastSent:            make(map[string]time.Time),
	}
}

func (a *Alerter) Check(m collector.Metrics) {
	if !a.cfg.Alert.Enabled {
		return
	}
	duration := time.Duration(a.cfg.Alert.Duration) * time.Second

	if a.cfg.Alert.CPU && m.CPU != nil {
		conditionMet := m.CPU.Usage >= a.cfg.Alert.CPUThreshold
		a.checkCondition("cpu", conditionMet, duration, func() {
			a.send("CPU", fmt.Sprintf("CPU使用率 %.1f%% 超过阈值 %.1f%%", m.CPU.Usage, a.cfg.Alert.CPUThreshold))
		})
	}

	if a.cfg.Alert.Memory && m.Memory != nil {
		conditionMet := m.Memory.Usage >= a.cfg.Alert.MemoryThreshold
		a.checkCondition("memory", conditionMet, duration, func() {
			a.send("内存", fmt.Sprintf("内存使用率 %.1f%% 超过阈值 %.1f%%", m.Memory.Usage, a.cfg.Alert.MemoryThreshold))
		})
	}

	if a.cfg.Alert.Disk && m.Disk != nil {
		conditionMet := m.Disk.Usage >= a.cfg.Alert.DiskThreshold
		a.checkCondition("disk", conditionMet, duration, func() {
			a.send("磁盘", fmt.Sprintf("磁盘使用率 %.1f%% 超过阈值 %.1f%%", m.Disk.Usage, a.cfg.Alert.DiskThreshold))
		})
	}

	if m.Network != nil {
		if a.cfg.Alert.NetworkUp {
			conditionMet := m.Network.Upload >= a.cfg.Alert.NetworkUpThreshold
			a.checkCondition("upload", conditionMet, duration, func() {
				a.send("网络上传", fmt.Sprintf("上传速率 %s 超过阈值 %s", formatBytes(m.Network.Upload), formatBytes(a.cfg.Alert.NetworkUpThreshold)))
			})
		}
		if a.cfg.Alert.NetworkDown {
			conditionMet := m.Network.Download >= a.cfg.Alert.NetworkDownThreshold
			a.checkCondition("download", conditionMet, duration, func() {
				a.send("网络下载", fmt.Sprintf("下载速率 %s 超过阈值 %s", formatBytes(m.Network.Download), formatBytes(a.cfg.Alert.NetworkDownThreshold)))
			})
		}
	}

	if m.DiskIO != nil {
		if a.cfg.Alert.DiskRead {
			conditionMet := m.DiskIO.ReadBytes >= a.cfg.Alert.DiskReadThreshold
			a.checkCondition("disk_read", conditionMet, duration, func() {
				a.send("磁盘读取", fmt.Sprintf("读取速率 %s 超过阈值 %s", formatBytes(m.DiskIO.ReadBytes), formatBytes(a.cfg.Alert.DiskReadThreshold)))
			})
		}
		if a.cfg.Alert.DiskWrite {
			conditionMet := m.DiskIO.WriteBytes >= a.cfg.Alert.DiskWriteThreshold
			a.checkCondition("disk_write", conditionMet, duration, func() {
				a.send("磁盘写入", fmt.Sprintf("写入速率 %s 超过阈值 %s", formatBytes(m.DiskIO.WriteBytes), formatBytes(a.cfg.Alert.DiskWriteThreshold)))
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

func (a *Alerter) send(subject, body string) {
	interval := time.Duration(a.cfg.Alert.Interval) * time.Second
	if lastSent, ok := a.lastSent[subject]; ok && time.Since(lastSent) < interval {
		return
	}
	a.lastSent[subject] = time.Now()

	smtpCfg := a.cfg.SMTP
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
		a.cfg.Name,
		body,
		serverTime,
		beijingTime,
	)
	encodedSubject := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("[监控报警][%s] %s", a.cfg.Name, subject)))
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
