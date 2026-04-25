//go:build windows

package manager

import "golang.org/x/sys/windows"

func readDiskUsageStats(path string) (diskUsageStats, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return diskUsageStats{}, err
	}

	var freeBytesAvailableToCaller uint64
	var totalBytes uint64
	var totalFreeBytes uint64
	if err := windows.GetDiskFreeSpaceEx(pathPtr, &freeBytesAvailableToCaller, &totalBytes, &totalFreeBytes); err != nil {
		return diskUsageStats{}, err
	}
	return diskUsageStats{
		totalBytes: totalBytes,
		freeBytes:  freeBytesAvailableToCaller,
	}, nil
}
