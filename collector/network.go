package collector

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Network reports per-interface aggregated rates in bytes/sec.
type Network struct {
	Upload       int64 `json:"upload"`
	Download     int64 `json:"download"`
	LanUpload    int64 `json:"lan_upload,omitempty"`
	LanDownload  int64 `json:"lan_download,omitempty"`
	WanUpload    int64 `json:"wan_upload,omitempty"`
	WanDownload  int64 `json:"wan_download,omitempty"`
}

type netCounter struct {
	rx int64
	tx int64
}

type classifierBackend int

const (
	classifierNone     classifierBackend = iota
	classifierNftables
	classifierIptables
)

var (
	lastNetworkStats   map[string]netCounter
	lastNetworkSampled time.Time
	networkMu          sync.Mutex
	totalUpload        int64
	totalDownload      int64

	physicalNICCache    map[string]bool
	physicalNICRefresh  time.Time
	physicalNICCacheMu  sync.Mutex
	physicalNICRefreshD = 5 * time.Minute

	classifier       classifierBackend
	classifierMu     sync.RWMutex
	lanWanMu         sync.Mutex
	lastLanIngress   int64
	lastLanEgress    int64
	lastWanIngress   int64
	lastWanEgress    int64
	totalLanUpload   int64
	totalLanDownload int64
	totalWanUpload   int64
	totalWanDownload int64
)

// nftables function pointers — default to exec-based implementations.
// On Linux, init() in nftables_netlink_linux.go replaces them with netlink.
var (
	initNftablesCounters   func() error                                          = initNftablesCountersExec
	cleanupNftablesCounters func()                                                = cleanupNftablesCountersExec
	readNftablesCounters   func() (lanIngress, lanEgress, wanIngress, wanEgress int64, err error) = readNftablesCountersExec
)

func InitNetwork() {
	networkMu.Lock()
	lastNetworkStats = make(map[string]netCounter)
	totalUpload = 0
	totalDownload = 0
	lastNetworkSampled = time.Time{}
	networkMu.Unlock()

	physicalNICCacheMu.Lock()
	physicalNICCache = make(map[string]bool)
	physicalNICRefresh = time.Time{}
	physicalNICCacheMu.Unlock()

	lanWanMu.Lock()
	lastLanIngress = 0
	lastLanEgress = 0
	lastWanIngress = 0
	lastWanEgress = 0
	totalLanUpload = 0
	totalLanDownload = 0
	totalWanUpload = 0
	totalWanDownload = 0
	lanWanMu.Unlock()
}

func EnableLanWanSplit() error {
	classifierMu.Lock()
	defer classifierMu.Unlock()

	if classifier != classifierNone {
		return nil
	}

	if err := initNftablesCounters(); err == nil {
		classifier = classifierNftables
		return nil
	}

	if err := initIptablesCounters(); err == nil {
		classifier = classifierIptables
		return nil
	}

	return fmt.Errorf("nftables and iptables are both unavailable")
}

func DisableLanWanSplit() {
	classifierMu.Lock()
	defer classifierMu.Unlock()

	switch classifier {
	case classifierNftables:
		cleanupNftablesCounters()
	case classifierIptables:
		cleanupIptablesCounters()
	}
	classifier = classifierNone
}

// ---------------------------------------------------------------------------
// nftables helpers — default exec-based implementations
// (replaced by netlink on Linux via nftables_netlink_linux.go)
// ---------------------------------------------------------------------------

var nftRegexp = regexp.MustCompile(`^\s*counter\s+(\S+)\s*\{\s*packets\s+\d+\s+bytes\s+(\d+)\s*\}`)

func runNft(args ...string) error {
	cmd := exec.Command("nft", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft %v: %w\n%s", args, err, out)
	}
	return nil
}

func initNftablesCountersExec() error {
	_ = runNft("delete", "table", "inet", "monitor")

	if err := runNft("add", "table", "inet", "monitor"); err != nil {
		return err
	}

	counters := []string{"lan_ingress", "lan_egress", "wan_ingress", "wan_egress"}
	for _, name := range counters {
		if err := runNft("add", "counter", "inet", "monitor", name); err != nil {
			return err
		}
	}

	if err := runNft("add", "chain", "inet", "monitor", "prerouting",
		"{", "type", "filter", "hook", "prerouting", "priority", "0;", "policy", "accept;", "}",
	); err != nil {
		return err
	}

	lanV4 := "{ 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 127.0.0.0/8 }"
	lanV6 := "{ fc00::/7, fe80::/10, ::1/128 }"

	// LAN rules must return so packets are not also counted as WAN.
	if err := runNft("add", "rule", "inet", "monitor", "prerouting",
		"ip", "saddr", lanV4, "counter", "name", "lan_ingress", "return",
	); err != nil {
		return err
	}
	if err := runNft("add", "rule", "inet", "monitor", "prerouting",
		"ip6", "saddr", lanV6, "counter", "name", "lan_ingress", "return",
	); err != nil {
		return err
	}
	if err := runNft("add", "rule", "inet", "monitor", "prerouting",
		"counter", "name", "wan_ingress",
	); err != nil {
		return err
	}

	if err := runNft("add", "chain", "inet", "monitor", "postrouting",
		"{", "type", "filter", "hook", "postrouting", "priority", "0;", "policy", "accept;", "}",
	); err != nil {
		return err
	}

	if err := runNft("add", "rule", "inet", "monitor", "postrouting",
		"ip", "daddr", lanV4, "counter", "name", "lan_egress", "return",
	); err != nil {
		return err
	}
	if err := runNft("add", "rule", "inet", "monitor", "postrouting",
		"ip6", "daddr", lanV6, "counter", "name", "lan_egress", "return",
	); err != nil {
		return err
	}
	if err := runNft("add", "rule", "inet", "monitor", "postrouting",
		"counter", "name", "wan_egress",
	); err != nil {
		return err
	}

	return nil
}

func cleanupNftablesCountersExec() {
	_ = runNft("delete", "table", "inet", "monitor")
}

func readNftablesCountersExec() (lanIngress, lanEgress, wanIngress, wanEgress int64, err error) {
	cmd := exec.Command("nft", "list", "table", "inet", "monitor")
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("nft list table: %w", err)
	}

	for _, line := range strings.Split(string(out), "\n") {
		if m := nftRegexp.FindStringSubmatch(line); m != nil {
			name := m[1]
			val, _ := strconv.ParseInt(m[2], 10, 64)
			switch name {
			case "lan_ingress":
				lanIngress = val
			case "lan_egress":
				lanEgress = val
			case "wan_ingress":
				wanIngress = val
			case "wan_egress":
				wanEgress = val
			}
		}
	}

	return lanIngress, lanEgress, wanIngress, wanEgress, nil
}

// ---------------------------------------------------------------------------
// iptables helpers
// ---------------------------------------------------------------------------

var lanV4CIDRs = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"169.254.0.0/16",
	"127.0.0.0/8",
}

var lanV6CIDRs = []string{
	"fc00::/7",
	"fe80::/10",
	"::1/128",
}

func runIptablesRaw(cmd string, args ...string) error {
	c := exec.Command(cmd, args...)
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %v: %w\n%s", cmd, args, err, out)
	}
	return nil
}

func initIptablesCounters() error {
	// Remove any stale state first
	cleanupIptablesCounters()

	// Create / flush custom chains
	chains := []string{"MONITOR_LAN_DOWN", "MONITOR_WAN_DOWN", "MONITOR_LAN_UP", "MONITOR_WAN_UP"}
	for _, ch := range chains {
		if err := runIptablesRaw("iptables", "-t", "mangle", "-N", ch); err != nil {
			_ = runIptablesRaw("iptables", "-t", "mangle", "-F", ch)
		}
	}

	// --- Ingress (download) classification via PREROUTING ---
	_ = runIptablesRaw("iptables", "-t", "mangle", "-D", "PREROUTING", "-j", "MONITOR_LAN_DOWN")
	if err := runIptablesRaw("iptables", "-t", "mangle", "-I", "PREROUTING", "1", "-j", "MONITOR_LAN_DOWN"); err != nil {
		return err
	}
	for _, cidr := range lanV4CIDRs {
		_ = runIptablesRaw("iptables", "-t", "mangle", "-A", "MONITOR_LAN_DOWN", "-s", cidr, "-j", "RETURN")
	}
	_ = runIptablesRaw("iptables", "-t", "mangle", "-A", "MONITOR_LAN_DOWN", "-j", "MONITOR_WAN_DOWN")
	_ = runIptablesRaw("iptables", "-t", "mangle", "-A", "MONITOR_WAN_DOWN", "-j", "RETURN")

	// --- Egress (upload) classification via POSTROUTING ---
	_ = runIptablesRaw("iptables", "-t", "mangle", "-D", "POSTROUTING", "-j", "MONITOR_LAN_UP")
	if err := runIptablesRaw("iptables", "-t", "mangle", "-I", "POSTROUTING", "1", "-j", "MONITOR_LAN_UP"); err != nil {
		return err
	}
	for _, cidr := range lanV4CIDRs {
		_ = runIptablesRaw("iptables", "-t", "mangle", "-A", "MONITOR_LAN_UP", "-d", cidr, "-j", "RETURN")
	}
	_ = runIptablesRaw("iptables", "-t", "mangle", "-A", "MONITOR_LAN_UP", "-j", "MONITOR_WAN_UP")
	_ = runIptablesRaw("iptables", "-t", "mangle", "-A", "MONITOR_WAN_UP", "-j", "RETURN")

	// --- IPv6 (best-effort) ---
	initIptables6()

	return nil
}

func initIptables6() {
	if _, err := exec.LookPath("ip6tables"); err != nil {
		return
	}

	chains := []string{"MONITOR_LAN_DOWN", "MONITOR_WAN_DOWN", "MONITOR_LAN_UP", "MONITOR_WAN_UP"}
	for _, ch := range chains {
		if err := runIptablesRaw("ip6tables", "-t", "mangle", "-N", ch); err != nil {
			_ = runIptablesRaw("ip6tables", "-t", "mangle", "-F", ch)
		}
	}

	_ = runIptablesRaw("ip6tables", "-t", "mangle", "-D", "PREROUTING", "-j", "MONITOR_LAN_DOWN")
	_ = runIptablesRaw("ip6tables", "-t", "mangle", "-I", "PREROUTING", "1", "-j", "MONITOR_LAN_DOWN")
	for _, cidr := range lanV6CIDRs {
		_ = runIptablesRaw("ip6tables", "-t", "mangle", "-A", "MONITOR_LAN_DOWN", "-s", cidr, "-j", "RETURN")
	}
	_ = runIptablesRaw("ip6tables", "-t", "mangle", "-A", "MONITOR_LAN_DOWN", "-j", "MONITOR_WAN_DOWN")
	_ = runIptablesRaw("ip6tables", "-t", "mangle", "-A", "MONITOR_WAN_DOWN", "-j", "RETURN")

	_ = runIptablesRaw("ip6tables", "-t", "mangle", "-D", "POSTROUTING", "-j", "MONITOR_LAN_UP")
	_ = runIptablesRaw("ip6tables", "-t", "mangle", "-I", "POSTROUTING", "1", "-j", "MONITOR_LAN_UP")
	for _, cidr := range lanV6CIDRs {
		_ = runIptablesRaw("ip6tables", "-t", "mangle", "-A", "MONITOR_LAN_UP", "-d", cidr, "-j", "RETURN")
	}
	_ = runIptablesRaw("ip6tables", "-t", "mangle", "-A", "MONITOR_LAN_UP", "-j", "MONITOR_WAN_UP")
	_ = runIptablesRaw("ip6tables", "-t", "mangle", "-A", "MONITOR_WAN_UP", "-j", "RETURN")
}

func cleanupIptablesCounters() {
	for _, cmd := range []string{"iptables", "ip6tables"} {
		_ = runIptablesRaw(cmd, "-t", "mangle", "-D", "PREROUTING", "-j", "MONITOR_LAN_DOWN")
		_ = runIptablesRaw(cmd, "-t", "mangle", "-D", "POSTROUTING", "-j", "MONITOR_LAN_UP")
		for _, ch := range []string{"MONITOR_LAN_DOWN", "MONITOR_WAN_DOWN", "MONITOR_LAN_UP", "MONITOR_WAN_UP"} {
			_ = runIptablesRaw(cmd, "-t", "mangle", "-F", ch)
			_ = runIptablesRaw(cmd, "-t", "mangle", "-X", ch)
		}
	}
}

func readIptablesCounters() (lanIngress, lanEgress, wanIngress, wanEgress int64, err error) {
	v4lanIn, v4wanIn, v4lanEg, v4wanEg, err := readSingleIptablesFamily("iptables")
	if err != nil {
		return 0, 0, 0, 0, err
	}

	v6lanIn, v6wanIn, v6lanEg, v6wanEg, _ := readSingleIptablesFamily("ip6tables")

	return v4lanIn + v6lanIn,
		v4lanEg + v6lanEg,
		v4wanIn + v6wanIn,
		v4wanEg + v6wanEg, nil
}

func readSingleIptablesFamily(cmd string) (lanIngress, wanIngress, lanEgress, wanEgress int64, err error) {
	lanIn, wanIn, err := parseIptablesChain(cmd, "MONITOR_LAN_DOWN")
	if err != nil {
		return 0, 0, 0, 0, err
	}
	lanEg, wanEg, err := parseIptablesChain(cmd, "MONITOR_LAN_UP")
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return lanIn, wanIn, lanEg, wanEg, nil
}

func parseIptablesChain(cmd, chain string) (lanBytes, wanBytes int64, err error) {
	c := exec.Command(cmd, "-t", "mangle", "-L", chain, "-v", "-n", "-x")
	out, err := c.Output()
	if err != nil {
		return 0, 0, fmt.Errorf("%s -L %s: %w", cmd, chain, err)
	}

	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		if _, err := strconv.ParseInt(parts[0], 10, 64); err != nil {
			continue // skip header lines
		}
		bytesVal, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		target := parts[2]
		if target == "RETURN" {
			lanBytes += bytesVal
		} else if target == "MONITOR_WAN_DOWN" || target == "MONITOR_WAN_UP" {
			wanBytes += bytesVal
		}
	}

	return lanBytes, wanBytes, nil
}

// ---------------------------------------------------------------------------
// Physical NIC detection
// ---------------------------------------------------------------------------

func scanPhysicalNICs() map[string]bool {
	out := make(map[string]bool)
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return out
	}
	for _, entry := range entries {
		iface := entry.Name()
		// VLAN (eth0.100) and macvlan (eth0@eth0) subinterfaces share the
		// parent's PCI device, so counting them would double the parent's
		// traffic.
		if strings.ContainsAny(iface, ".@") {
			continue
		}
		_, hasDevice := os.Stat("/sys/class/net/" + iface + "/device")
		_, isBondMaster := os.Stat("/sys/class/net/" + iface + "/bonding")
		if hasDevice != nil && isBondMaster != nil {
			continue
		}
		// Enslaved interfaces (bond members) report traffic already counted
		// on the bond itself.
		if _, err := os.Stat("/sys/class/net/" + iface + "/master"); err == nil {
			continue
		}
		out[iface] = true
	}
	return out
}

func isPhysicalNIC(iface string) bool {
	physicalNICCacheMu.Lock()
	defer physicalNICCacheMu.Unlock()
	now := time.Now()
	if physicalNICCache == nil || now.Sub(physicalNICRefresh) > physicalNICRefreshD {
		physicalNICCache = scanPhysicalNICs()
		physicalNICRefresh = now
	}
	return physicalNICCache[iface]
}

// ---------------------------------------------------------------------------
// Main collection
// ---------------------------------------------------------------------------

func CollectNetwork() (*Network, error) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Parse counters without holding networkMu so slow /proc reads and
	// physical-NIC cache refresh do not block hourly total resets.
	current := make(map[string]netCounter)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(nil, 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) < 10 {
			continue
		}

		iface := strings.Trim(parts[0], ":")
		if !isPhysicalNIC(iface) {
			continue
		}

		rx, _ := strconv.ParseInt(parts[1], 10, 64)
		tx, _ := strconv.ParseInt(parts[9], 10, 64)
		current[iface] = netCounter{rx: rx, tx: tx}
	}

	now := time.Now()
	var deltaUpload, deltaDownload int64
	var counterRegressed bool
	var regressedIface string

	networkMu.Lock()
	for iface, cur := range current {
		last, exists := lastNetworkStats[iface]
		if exists {
			if cur.rx < last.rx || cur.tx < last.tx {
				counterRegressed = true
				if regressedIface == "" {
					regressedIface = iface
				}
			} else {
				deltaDownload += cur.rx - last.rx
				deltaUpload += cur.tx - last.tx
			}
		}
		lastNetworkStats[iface] = cur
	}

	// On counter regression (NIC reset, 32-bit wraparound, hot-plug),
	// drop this round's delta entirely — reseed from the new baseline.
	if counterRegressed {
		log.Printf("网络计数器回退，跳过本轮速率计算 (iface=%s)", regressedIface)
		lastNetworkSampled = now
		networkMu.Unlock()
		return &Network{}, nil
	}

	totalUpload += deltaUpload
	totalDownload += deltaDownload

	// elapsed uses Go's monotonic clock component of time.Time.
	const maxElapsedSeconds = 600.0

	var rateUp, rateDown int64
	var elapsed float64
	if !lastNetworkSampled.IsZero() {
		elapsed = now.Sub(lastNetworkSampled).Seconds()
		if elapsed > 0 && elapsed <= maxElapsedSeconds {
			rateUp = int64(float64(deltaUpload) / elapsed)
			rateDown = int64(float64(deltaDownload) / elapsed)
		} else if elapsed > maxElapsedSeconds {
			log.Printf("网络采集间隔异常 (%.1fs)，跳过本轮速率计算", elapsed)
			elapsed = 0
		}
	}
	lastNetworkSampled = now
	networkMu.Unlock()

	n := &Network{
		Upload:   rateUp,
		Download: rateDown,
	}

	if elapsed <= 0 {
		return n, nil
	}

	// Hold classifierMu.RLock across the counter read so the classifier
	// backend (and the nftables netlink state it owns) cannot be torn down
	// concurrently by EnableLanWanSplit/DisableLanWanSplit from an HTTP
	// config-reload goroutine. Without this, readNftablesCountersNetlink
	// would race on the package-level nftState pointer.
	classifierMu.RLock()
	cl := classifier
	var lanRcv, lanSnd, wanRcv, wanSnd int64
	var readErr error
	classified := true
	switch cl {
	case classifierNftables:
		lanRcv, lanSnd, wanRcv, wanSnd, readErr = readNftablesCounters()
	case classifierIptables:
		lanRcv, lanSnd, wanRcv, wanSnd, readErr = readIptablesCounters()
	default:
		classified = false
	}
	classifierMu.RUnlock()

	if !classified || readErr != nil {
		return n, nil
	}

	lanWanMu.Lock()
	lanWanRegressed := (lastLanIngress > 0 && lanRcv < lastLanIngress) ||
		(lastLanEgress > 0 && lanSnd < lastLanEgress) ||
		(lastWanIngress > 0 && wanRcv < lastWanIngress) ||
		(lastWanEgress > 0 && wanSnd < lastWanEgress)

	if lanWanRegressed {
		log.Println("LAN/WAN 计数器回退，跳过本轮速率计算")
	} else {
		if lastLanIngress > 0 && lanRcv >= lastLanIngress {
			delta := lanRcv - lastLanIngress
			totalLanDownload += delta
			n.LanDownload = int64(float64(delta) / elapsed)
		}
		if lastLanEgress > 0 && lanSnd >= lastLanEgress {
			delta := lanSnd - lastLanEgress
			totalLanUpload += delta
			n.LanUpload = int64(float64(delta) / elapsed)
		}
		if lastWanIngress > 0 && wanRcv >= lastWanIngress {
			delta := wanRcv - lastWanIngress
			totalWanDownload += delta
			n.WanDownload = int64(float64(delta) / elapsed)
		}
		if lastWanEgress > 0 && wanSnd >= lastWanEgress {
			delta := wanSnd - lastWanEgress
			totalWanUpload += delta
			n.WanUpload = int64(float64(delta) / elapsed)
		}
	}

	lastLanIngress = lanRcv
	lastLanEgress = lanSnd
	lastWanIngress = wanRcv
	lastWanEgress = wanSnd
	lanWanMu.Unlock()

	return n, nil
}

func GetHourlyTotals() (int64, int64, int64, int64, int64, int64) {
	networkMu.Lock()
	upload := totalUpload
	download := totalDownload
	networkMu.Unlock()

	lanWanMu.Lock()
	lanUp := totalLanUpload
	lanDown := totalLanDownload
	wanUp := totalWanUpload
	wanDown := totalWanDownload
	lanWanMu.Unlock()

	return upload, download, lanUp, lanDown, wanUp, wanDown
}

func GetHourlyTotalsAndReset() (int64, int64, int64, int64, int64, int64) {
	networkMu.Lock()
	upload := totalUpload
	download := totalDownload
	totalUpload = 0
	totalDownload = 0
	networkMu.Unlock()

	lanWanMu.Lock()
	lanUp := totalLanUpload
	lanDown := totalLanDownload
	wanUp := totalWanUpload
	wanDown := totalWanDownload
	totalLanUpload = 0
	totalLanDownload = 0
	totalWanUpload = 0
	totalWanDownload = 0
	lanWanMu.Unlock()

	return upload, download, lanUp, lanDown, wanUp, wanDown
}
