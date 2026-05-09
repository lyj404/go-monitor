package alerter

import (
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"go-monitor/collector"
	"go-monitor/config"
)

const (
	smtpDialTimeout    = 10 * time.Second
	smtpOverallTimeout = 30 * time.Second
	defaultQueueSize   = 32
	defaultWorkers     = 2
)

type emailJob struct {
	subject string
	body    string
	cfg     config.Config
}

type Alerter struct {
	mu                  sync.Mutex
	conditionStartTimes map[string]time.Time

	sendMu   sync.Mutex
	lastSent map[string]time.Time

	jobs     chan emailJob
	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

func New() *Alerter {
	a := &Alerter{
		conditionStartTimes: make(map[string]time.Time),
		lastSent:            make(map[string]time.Time),
		jobs:                make(chan emailJob, defaultQueueSize),
		stopCh:              make(chan struct{}),
	}
	for i := 0; i < defaultWorkers; i++ {
		a.wg.Add(1)
		go a.worker()
	}
	return a
}

func (a *Alerter) Close() {
	a.stopOnce.Do(func() {
		close(a.stopCh)
		a.wg.Wait()
	})
}

func (a *Alerter) CheckWithConfig(m collector.Metrics, cfg config.Config) {
	if !cfg.Alert.Enabled {
		return
	}
	duration := time.Duration(cfg.Alert.Duration) * time.Second

	if cfg.Alert.CPU && m.CPU != nil {
		if a.shouldFire("cpu", m.CPU.Usage >= cfg.Alert.CPUThreshold, duration) {
			a.send("CPU", fmt.Sprintf("CPU使用率 %.1f%% 超过阈值 %.1f%%", m.CPU.Usage, cfg.Alert.CPUThreshold), cfg)
		}
	}

	if cfg.Alert.Memory && m.Memory != nil {
		if a.shouldFire("memory", m.Memory.Usage >= cfg.Alert.MemoryThreshold, duration) {
			a.send("内存", fmt.Sprintf("内存使用率 %.1f%% 超过阈值 %.1f%%", m.Memory.Usage, cfg.Alert.MemoryThreshold), cfg)
		}
	}

	if cfg.Alert.Disk && m.Disk != nil {
		if a.shouldFire("disk", m.Disk.Usage >= cfg.Alert.DiskThreshold, duration) {
			a.send("磁盘", fmt.Sprintf("磁盘使用率 %.1f%% 超过阈值 %.1f%%", m.Disk.Usage, cfg.Alert.DiskThreshold), cfg)
		}
	}

	if m.Network != nil {
		if cfg.Alert.NetworkUp {
			if a.shouldFire("upload", m.Network.Upload >= cfg.Alert.NetworkUpThreshold, duration) {
				a.send("网络上传", fmt.Sprintf("上传速率 %s/s 超过阈值 %s/s", formatBytes(m.Network.Upload), formatBytes(cfg.Alert.NetworkUpThreshold)), cfg)
			}
		}
		if cfg.Alert.NetworkDown {
			if a.shouldFire("download", m.Network.Download >= cfg.Alert.NetworkDownThreshold, duration) {
				a.send("网络下载", fmt.Sprintf("下载速率 %s/s 超过阈值 %s/s", formatBytes(m.Network.Download), formatBytes(cfg.Alert.NetworkDownThreshold)), cfg)
			}
		}
	}

	if m.DiskIO != nil {
		if cfg.Alert.DiskRead {
			if a.shouldFire("disk_read", m.DiskIO.ReadBytes >= cfg.Alert.DiskReadThreshold, duration) {
				a.send("磁盘读取", fmt.Sprintf("读取速率 %s/s 超过阈值 %s/s", formatBytes(m.DiskIO.ReadBytes), formatBytes(cfg.Alert.DiskReadThreshold)), cfg)
			}
		}
		if cfg.Alert.DiskWrite {
			if a.shouldFire("disk_write", m.DiskIO.WriteBytes >= cfg.Alert.DiskWriteThreshold, duration) {
				a.send("磁盘写入", fmt.Sprintf("写入速率 %s/s 超过阈值 %s/s", formatBytes(m.DiskIO.WriteBytes), formatBytes(cfg.Alert.DiskWriteThreshold)), cfg)
			}
		}
	}
}

// shouldFire returns true when the condition has been continuously met for
// the configured duration. The state is reset on success so that re-firing
// requires another full duration window — limiting noise. The actual
// "minimum interval between emails" is enforced separately in send().
func (a *Alerter) shouldFire(name string, conditionMet bool, duration time.Duration) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	startTime, exists := a.conditionStartTimes[name]

	if !conditionMet {
		delete(a.conditionStartTimes, name)
		return false
	}

	if !exists {
		a.conditionStartTimes[name] = time.Now()
		return false
	}

	if time.Since(startTime) >= duration {
		delete(a.conditionStartTimes, name)
		return true
	}
	return false
}

func (a *Alerter) send(subject, body string, cfg config.Config) {
	interval := time.Duration(cfg.Alert.Interval) * time.Second

	a.sendMu.Lock()
	if lastSent, ok := a.lastSent[subject]; ok && time.Since(lastSent) < interval {
		a.sendMu.Unlock()
		return
	}
	a.lastSent[subject] = time.Now()
	a.sendMu.Unlock()

	job := emailJob{subject: subject, body: body, cfg: cfg}
	select {
	case a.jobs <- job:
	default:
		log.Printf("报警队列已满，丢弃邮件 [%s]", subject)
	}
}

func (a *Alerter) worker() {
	defer a.wg.Done()
	for {
		select {
		case job, ok := <-a.jobs:
			if !ok {
				return
			}
			a.deliver(job)
		case <-a.stopCh:
			return
		}
	}
}

func (a *Alerter) deliver(job emailJob) {
	subject := job.subject
	body := job.body
	cfg := job.cfg

	smtpCfg := cfg.SMTP
	addr := fmt.Sprintf("%s:%d", smtpCfg.Host, smtpCfg.Port)
	toList := strings.Join(smtpCfg.To, ", ")

	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
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

	if err := sendMailWithTimeout(addr, smtpCfg, []byte(msg)); err != nil {
		log.Printf("报警邮件发送失败 [%s]: %v", subject, err)
	} else {
		log.Printf("报警邮件已发送 [%s]: %s", subject, body)
	}
}

// sendMailWithTimeout dials the SMTP server with a connect timeout and a
// hard overall deadline so a hung server cannot leak goroutines or pile up
// further alerts.
func sendMailWithTimeout(addr string, smtpCfg config.SMTPConfig, msg []byte) error {
	if len(smtpCfg.To) == 0 {
		return errors.New("收件人列表为空")
	}

	conn, err := net.DialTimeout("tcp", addr, smtpDialTimeout)
	if err != nil {
		return fmt.Errorf("拨号 SMTP 失败: %w", err)
	}
	if err := conn.SetDeadline(time.Now().Add(smtpOverallTimeout)); err != nil {
		conn.Close()
		return err
	}

	c, err := smtp.NewClient(conn, smtpCfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP 客户端: %w", err)
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: smtpCfg.Host}); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}

	if smtpCfg.User != "" {
		auth := smtp.PlainAuth("", smtpCfg.User, smtpCfg.Pass, smtpCfg.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("SMTP 认证: %w", err)
		}
	}

	if err := c.Mail(smtpCfg.User); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	for _, to := range smtpCfg.To {
		if err := c.Rcpt(to); err != nil {
			return fmt.Errorf("RCPT TO %s: %w", to, err)
		}
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	return c.Quit()
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
