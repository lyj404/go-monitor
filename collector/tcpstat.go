package collector

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

type TCPStat struct {
	Established int `json:"established"`
	SynSent     int `json:"syn_sent"`
	SynRecv     int `json:"syn_recv"`
	FinWait1    int `json:"fin_wait1"`
	FinWait2    int `json:"fin_wait2"`
	TimeWait    int `json:"time_wait"`
	Close       int `json:"close"`
	CloseWait   int `json:"close_wait"`
	LastAck     int `json:"last_ack"`
	Listen      int `json:"listen"`
	Closing     int `json:"closing"`
	Unknown     int `json:"unknown"`
}

// tcpStateCode maps /proc/net/tcp state hex codes to field names.
// See kernel source: include/net/tcp_states.h
func tcpStateCode(code int) string {
	switch code {
	case 1:
		return "established"
	case 2:
		return "syn_sent"
	case 3:
		return "syn_recv"
	case 4:
		return "fin_wait1"
	case 5:
		return "fin_wait2"
	case 6:
		return "time_wait"
	case 7:
		return "close"
	case 8:
		return "close_wait"
	case 9:
		return "last_ack"
	case 0x0A:
		return "listen"
	case 0x0B:
		return "closing"
	default:
		return "unknown"
	}
}

func CollectTCPStat() (*TCPStat, error) {
	stat := &TCPStat{}

	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		if err := parseTCPFile(path, stat); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return stat, err
		}
	}

	return stat, nil
}

func parseTCPFile(path string, stat *TCPStat) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Skip header line
	if !scanner.Scan() {
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		// Column 3 (0-indexed) is the state in hex
		stateHex, err := strconv.ParseInt(fields[3], 16, 64)
		if err != nil {
			continue
		}
		switch tcpStateCode(int(stateHex)) {
		case "established":
			stat.Established++
		case "syn_sent":
			stat.SynSent++
		case "syn_recv":
			stat.SynRecv++
		case "fin_wait1":
			stat.FinWait1++
		case "fin_wait2":
			stat.FinWait2++
		case "time_wait":
			stat.TimeWait++
		case "close":
			stat.Close++
		case "close_wait":
			stat.CloseWait++
		case "last_ack":
			stat.LastAck++
		case "listen":
			stat.Listen++
		case "closing":
			stat.Closing++
		default:
			stat.Unknown++
		}
	}

	return scanner.Err()
}
