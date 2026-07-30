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

	"github.com/lyj404/go-monitor/collector"
	"github.com/lyj404/go-monitor/config"
)

const (
	smtpDialTimeout    = 10 * time.Second
	smtpOverallTimeout = 30 * time.Second
	defaultQueueSize   = 32
	defaultWorkers     = 2
	stateTTL           = 24 * time.Hour
	// closeTimeout must comfortably exceed a single SMTP delivery so an
	// in-flight worker is not abandoned mid-send during shutdown. Sized to
	// smtpOverallTimeout plus headroom so the two stay coupled if SMTP
	// timeouts are tuned later.
	closeTimeout = smtpOverallTimeout + 5*time.Second
)

type emailJob struct {
	subject string
	body    string
	cfg     config.Snapshot
}

type Alerter struct {
	mu                  sync.Mutex
	conditionStartTimes map[string]time.Time

	sendMu   sync.Mutex
	lastSent map[string]time.Time

	jobs     chan emailJob
	stopOnce sync.Once
	stopCh   chan struct{}
	closed   bool
	wg       sync.WaitGroup

	// onAlert is called (if non-nil) every time an alert fires.
	// Parameters: alertType, message, currentValue, threshold.
	onAlert func(alertType, message, currentValue, threshold string)
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

// SetOnAlert registers a callback that fires when an alert email is queued.
// The callback receives (alertType, message, currentValue, threshold).
func (a *Alerter) SetOnAlert(fn func(alertType, message, currentValue, threshold string)) {
	a.mu.Lock()
	a.onAlert = fn
	a.mu.Unlock()
}

func (a *Alerter) notifyAlert(alertType, message, currentValue, threshold string) {
	a.mu.Lock()
	fn := a.onAlert
	a.mu.Unlock()
	if fn != nil {
		fn(alertType, message, currentValue, threshold)
	}
}

func (a *Alerter) Close() {
	a.stopOnce.Do(func() {
		a.sendMu.Lock()
		a.closed = true
		a.sendMu.Unlock()
		close(a.stopCh)

		done := make(chan struct{})
		go func() {
			a.wg.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(closeTimeout):
			log.Printf("报警 worker 关闭超时 (%.0fs)，存在进行中的 SMTP 投递", closeTimeout.Seconds())
		}
	})
}

func (a *Alerter) CheckWithConfig(m collector.Metrics, cfg config.Snapshot) {
	if !cfg.Alert.Enabled {
		return
	}
	duration := time.Duration(cfg.Alert.Duration) * time.Second

	if cfg.Alert.CPU && m.CPU != nil {
		if a.shouldFire("cpu", m.CPU.Usage >= cfg.Alert.CPUThreshold, duration) {
			msg := fmt.Sprintf("CPU使用率 %.1f%% 超过阈值 %.1f%%", m.CPU.Usage, cfg.Alert.CPUThreshold)
			if a.send("CPU", msg, cfg) {
				a.notifyAlert("CPU", msg, fmt.Sprintf("%.1f%%", m.CPU.Usage), fmt.Sprintf("%.1f%%", cfg.Alert.CPUThreshold))
			}
		}
	}

	if cfg.Alert.Memory && m.Memory != nil {
		if a.shouldFire("memory", m.Memory.Usage >= cfg.Alert.MemoryThreshold, duration) {
			msg := fmt.Sprintf("内存使用率 %.1f%% 超过阈值 %.1f%%", m.Memory.Usage, cfg.Alert.MemoryThreshold)
			if a.send("内存", msg, cfg) {
				a.notifyAlert("内存", msg, fmt.Sprintf("%.1f%%", m.Memory.Usage), fmt.Sprintf("%.1f%%", cfg.Alert.MemoryThreshold))
			}
		}
	}

	if cfg.Alert.Disk && m.Disk != nil {
		if a.shouldFire("disk", m.Disk.Usage >= cfg.Alert.DiskThreshold, duration) {
			msg := fmt.Sprintf("磁盘使用率 %.1f%% 超过阈值 %.1f%%", m.Disk.Usage, cfg.Alert.DiskThreshold)
			if a.send("磁盘", msg, cfg) {
				a.notifyAlert("磁盘", msg, fmt.Sprintf("%.1f%%", m.Disk.Usage), fmt.Sprintf("%.1f%%", cfg.Alert.DiskThreshold))
			}
		}
	}

	if m.Network != nil {
		if cfg.Alert.NetworkUp {
			if a.shouldFire("upload", m.Network.Upload >= cfg.Alert.NetworkUpThreshold, duration) {
				msg := fmt.Sprintf("上传速率 %s/s 超过阈值 %s/s", formatBytes(m.Network.Upload), formatBytes(cfg.Alert.NetworkUpThreshold))
				if a.send("网络上传", msg, cfg) {
					a.notifyAlert("网络上传", msg, formatBytes(m.Network.Upload)+"/s", formatBytes(cfg.Alert.NetworkUpThreshold)+"/s")
				}
			}
		}
		if cfg.Alert.NetworkDown {
			if a.shouldFire("download", m.Network.Download >= cfg.Alert.NetworkDownThreshold, duration) {
				msg := fmt.Sprintf("下载速率 %s/s 超过阈值 %s/s", formatBytes(m.Network.Download), formatBytes(cfg.Alert.NetworkDownThreshold))
				if a.send("网络下载", msg, cfg) {
					a.notifyAlert("网络下载", msg, formatBytes(m.Network.Download)+"/s", formatBytes(cfg.Alert.NetworkDownThreshold)+"/s")
				}
			}
		}
	}

	if m.DiskIO != nil {
		if cfg.Alert.DiskRead {
			if a.shouldFire("disk_read", m.DiskIO.ReadBytes >= cfg.Alert.DiskReadThreshold, duration) {
				msg := fmt.Sprintf("读取速率 %s/s 超过阈值 %s/s", formatBytes(m.DiskIO.ReadBytes), formatBytes(cfg.Alert.DiskReadThreshold))
				if a.send("磁盘读取", msg, cfg) {
					a.notifyAlert("磁盘读取", msg, formatBytes(m.DiskIO.ReadBytes)+"/s", formatBytes(cfg.Alert.DiskReadThreshold)+"/s")
				}
			}
		}
		if cfg.Alert.DiskWrite {
			if a.shouldFire("disk_write", m.DiskIO.WriteBytes >= cfg.Alert.DiskWriteThreshold, duration) {
				msg := fmt.Sprintf("写入速率 %s/s 超过阈值 %s/s", formatBytes(m.DiskIO.WriteBytes), formatBytes(cfg.Alert.DiskWriteThreshold))
				if a.send("磁盘写入", msg, cfg) {
					a.notifyAlert("磁盘写入", msg, formatBytes(m.DiskIO.WriteBytes)+"/s", formatBytes(cfg.Alert.DiskWriteThreshold)+"/s")
				}
			}
		}
	}

	if cfg.Alert.Process && m.Process != nil {
		if a.shouldFire("process", m.Process.Count >= cfg.Alert.ProcessThreshold, duration) {
			msg := fmt.Sprintf("进程数 %d 超过阈值 %d", m.Process.Count, cfg.Alert.ProcessThreshold)
			if a.send("进程数", msg, cfg) {
				a.notifyAlert("进程数", msg, fmt.Sprintf("%d", m.Process.Count), fmt.Sprintf("%d", cfg.Alert.ProcessThreshold))
			}
		}
	}

	if cfg.Alert.CPUTemp && m.CPUTemp != nil {
		if a.shouldFire("cpu_temp", m.CPUTemp.Temp >= cfg.Alert.CPUTempThreshold, duration) {
			msg := fmt.Sprintf("CPU温度 %.1f°C 超过阈值 %.1f°C", m.CPUTemp.Temp, cfg.Alert.CPUTempThreshold)
			if a.send("CPU温度", msg, cfg) {
				a.notifyAlert("CPU温度", msg, fmt.Sprintf("%.1f°C", m.CPUTemp.Temp), fmt.Sprintf("%.1f°C", cfg.Alert.CPUTempThreshold))
			}
		}
	}

	if cfg.Alert.CloseWait && m.TCPStat != nil {
		if a.shouldFire("close_wait", m.TCPStat.CloseWait >= cfg.Alert.CloseWaitThreshold, duration) {
			msg := fmt.Sprintf("CLOSE_WAIT连接数 %d 超过阈值 %d", m.TCPStat.CloseWait, cfg.Alert.CloseWaitThreshold)
			if a.send("TCP CLOSE_WAIT", msg, cfg) {
				a.notifyAlert("TCP CLOSE_WAIT", msg, fmt.Sprintf("%d", m.TCPStat.CloseWait), fmt.Sprintf("%d", cfg.Alert.CloseWaitThreshold))
			}
		}
	}
}

// shouldFire returns true when the condition has been continuously met for
// the configured duration. duration <= 0 fires on the first sample that meets
// the condition. The state is reset on success so that re-firing requires
// another full duration window — limiting noise. The actual "minimum interval
// between emails" is enforced separately in send().
func (a *Alerter) shouldFire(name string, conditionMet bool, duration time.Duration) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneConditionStateLocked(time.Now())

	startTime, exists := a.conditionStartTimes[name]

	if !conditionMet {
		delete(a.conditionStartTimes, name)
		return false
	}

	if !exists {
		if duration <= 0 {
			return true
		}
		a.conditionStartTimes[name] = time.Now()
		return false
	}

	if time.Since(startTime) >= duration {
		delete(a.conditionStartTimes, name)
		return true
	}
	return false
}

// send enqueues an alert email. Returns true when the job was accepted so
// callers can persist matching alert history only for delivered attempts.
func (a *Alerter) send(subject, body string, cfg config.Snapshot) bool {
	interval := time.Duration(cfg.Alert.Interval) * time.Second
	now := time.Now()

	a.sendMu.Lock()
	if a.closed {
		a.sendMu.Unlock()
		return false
	}
	a.pruneLastSentLocked(now)
	if lastSent, ok := a.lastSent[subject]; ok && now.Sub(lastSent) < interval {
		a.sendMu.Unlock()
		return false
	}
	a.lastSent[subject] = now
	a.sendMu.Unlock()

	job := emailJob{subject: subject, body: body, cfg: cfg}
	select {
	case a.jobs <- job:
		return true
	default:
		// Queue is full. Keep lastSent[subject] so the next tick doesn't
		// immediately retry and pile up more drops — wait out the interval.
		log.Printf("报警队列已满，丢弃邮件 [%s]，等待下一个发送窗口", subject)
		return false
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
			// Drain queued jobs so in-flight alerts are not silently dropped
			// on shutdown (bounded by Close's overall timeout via wg.Wait).
			for {
				select {
				case job, ok := <-a.jobs:
					if !ok {
						return
					}
					a.deliver(job)
				default:
					return
				}
			}
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

	var bodyBuf strings.Builder
	bodyBuf.Grow(len(cfg.Name) + len(body) + len(serverTime) + len(beijingTime) + 40)
	bodyBuf.WriteString("服务器: ")
	bodyBuf.WriteString(cfg.Name)
	bodyBuf.WriteByte('\n')
	bodyBuf.WriteString(body)
	bodyBuf.WriteString("\n服务器时间: ")
	bodyBuf.WriteString(serverTime)
	bodyBuf.WriteString("\n北京时间: ")
	bodyBuf.WriteString(beijingTime)
	emailBody := bodyBuf.String()

	var subjBuf strings.Builder
	subjBuf.Grow(len("[监控报警][") + len(cfg.Name) + len("] ") + len(subject))
	subjBuf.WriteString("[监控报警][")
	subjBuf.WriteString(cfg.Name)
	subjBuf.WriteString("] ")
	subjBuf.WriteString(subject)
	encodedSubject := base64.StdEncoding.EncodeToString([]byte(subjBuf.String()))

	var msgBuf strings.Builder
	msgBuf.Grow(len(smtpCfg.User) + len(toList) + len(encodedSubject) + len(emailBody) + 96)
	msgBuf.WriteString("From: ")
	msgBuf.WriteString(smtpCfg.User)
	msgBuf.WriteString("\r\nTo: ")
	msgBuf.WriteString(toList)
	msgBuf.WriteString("\r\nSubject: =?UTF-8?B?")
	msgBuf.WriteString(encodedSubject)
	msgBuf.WriteString("?=\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n")
	msgBuf.WriteString(emailBody)

	if err := sendMailWithTimeout(addr, smtpCfg, []byte(msgBuf.String())); err != nil {
		log.Printf("报警邮件发送失败 [%s]: %v", subject, err)
	} else {
		log.Printf("报警邮件已发送 [%s]: %s", subject, body)
	}
}

func (a *Alerter) pruneConditionStateLocked(now time.Time) {
	for name, startedAt := range a.conditionStartTimes {
		if now.Sub(startedAt) > stateTTL {
			delete(a.conditionStartTimes, name)
		}
	}
}

func (a *Alerter) pruneLastSentLocked(now time.Time) {
	for subject, sentAt := range a.lastSent {
		if now.Sub(sentAt) > stateTTL {
			delete(a.lastSent, subject)
		}
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
