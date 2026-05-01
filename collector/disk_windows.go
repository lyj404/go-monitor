//go:build windows

package collector

import (
	"golang.org/x/sys/windows"
)

type Disk struct {
	Total     uint64  `json:"total"`
	Used      uint64  `json:"used"`
	Available uint64  `json:"available"`
	Usage     float64 `json:"usage"`
}

func CollectDisk() (*Disk, error) {
	var freeBytesAvailable, totalNumberOfBytes, totalNumberOfFreeBytes uint64
	pathPtr, err := windows.UTF16PtrFromString("C:")
	if err != nil {
		return nil, err
	}
	err = windows.GetDiskFreeSpaceEx(pathPtr, &freeBytesAvailable, &totalNumberOfBytes, &totalNumberOfFreeBytes)
	if err != nil {
		return nil, err
	}

	used := totalNumberOfBytes - totalNumberOfFreeBytes
	usage := float64(used) / float64(totalNumberOfBytes) * 100

	return &Disk{
		Total:     totalNumberOfBytes,
		Used:      used,
		Available: totalNumberOfFreeBytes,
		Usage:     usage,
	}, nil
}
