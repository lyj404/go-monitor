package collector

import (
	"os"
	"unicode"
)

type Process struct {
	Count int `json:"count"`
}

func CollectProcess() (*Process, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}

	count := 0
	for _, e := range entries {
		name := e.Name()
		if len(name) > 0 && unicode.IsDigit(rune(name[0])) {
			count++
		}
	}

	return &Process{Count: count}, nil
}
