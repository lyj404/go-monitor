package collector

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

type SelfMem struct {
	RSS uint64 `json:"rss"` // KB
}

func CollectSelfMem() (*SelfMem, error) {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "VmRSS:") {
			// Format: "VmRSS:    12345 kB"
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				val, err := strconv.ParseUint(parts[1], 10, 64)
				if err == nil {
					return &SelfMem{RSS: val}, nil
				}
			}
		}
	}

	return &SelfMem{RSS: 0}, nil
}
