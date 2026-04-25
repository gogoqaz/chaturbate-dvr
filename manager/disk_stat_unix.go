//go:build !windows

package manager

import (
	"fmt"
	"math/bits"
	"syscall"
)

func readDiskUsageStats(path string) (diskUsageStats, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return diskUsageStats{}, err
	}

	totalBytes, ok := multiplyStatfsBytes(stat.Blocks, uint64(stat.Bsize))
	if !ok {
		return diskUsageStats{}, fmt.Errorf("total bytes overflow")
	}
	freeBytes, ok := multiplyStatfsBytes(stat.Bavail, uint64(stat.Bsize))
	if !ok {
		return diskUsageStats{}, fmt.Errorf("free bytes overflow")
	}
	return diskUsageStats{
		totalBytes: totalBytes,
		freeBytes:  freeBytes,
	}, nil
}

func multiplyStatfsBytes(blocks uint64, blockSize uint64) (uint64, bool) {
	if blockSize == 0 {
		return 0, false
	}
	hi, lo := bits.Mul64(blocks, blockSize)
	if hi != 0 {
		return 0, false
	}
	return lo, true
}
