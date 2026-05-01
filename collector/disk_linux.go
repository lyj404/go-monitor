//go:build linux

package collector

import (
	"syscall"
)

type Disk struct {
	Total     uint64  `json:"total"`
	Used      uint64  `json:"used"`
	Available uint64  `json:"available"`
	Usage     float64 `json:"usage"`
}

func CollectDisk() (*Disk, error) {
	disk := &Disk{}

	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return nil, err
	}

	disk.Total = stat.Blocks * uint64(stat.Bsize)
	disk.Available = stat.Bavail * uint64(stat.Bsize)
	disk.Used = disk.Total - disk.Available

	if disk.Total > 0 {
		disk.Usage = float64(disk.Used) / float64(disk.Total) * 100
	}

	return disk, nil
}
