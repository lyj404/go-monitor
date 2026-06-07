package collector

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

type Uptime struct {
	Seconds float64 `json:"seconds"`
}

func CollectUptime() (*Uptime, error) {
	f, err := os.Open("/proc/uptime")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return &Uptime{}, nil
	}

	parts := strings.Fields(scanner.Text())
	if len(parts) < 1 {
		return &Uptime{}, nil
	}

	sec, _ := strconv.ParseFloat(parts[0], 64)
	return &Uptime{Seconds: sec}, nil
}
