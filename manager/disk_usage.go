package manager

import (
	"bytes"
	"fmt"
	"html/template"
	"math/bits"
	"os"
	"path/filepath"

	"github.com/teacat/chaturbate-dvr/entity"
	"github.com/teacat/chaturbate-dvr/internal"
	"github.com/teacat/chaturbate-dvr/server"
)

const (
	diskWarningFreePercent = 10
	diskWarningFreeBytes   = 20 * 1024 * 1024 * 1024

	defaultRecordingPattern = "videos/{{.Username}}_{{.Year}}-{{.Month}}-{{.Day}}_{{.Hour}}-{{.Minute}}-{{.Second}}{{if .Sequence}}_{{.Sequence}}{{end}}"
)

type diskUsageStats struct {
	totalBytes uint64
	freeBytes  uint64
}

var diskUsageStatsFn = readDiskUsageStats

// DiskUsageInfo returns disk usage details for the current recording target.
func (m *Manager) DiskUsageInfo() *entity.DiskUsageInfo {
	return buildDiskUsageInfo(m.activeRecordingDir())
}

func (m *Manager) activeRecordingDir() string {
	if m != nil {
		var activeDir string
		m.recordingDirs.Range(func(_, value any) bool {
			if dir, ok := value.(string); ok && dir != "" {
				activeDir = dir
				return false
			}
			return true
		})
		if activeDir != "" {
			return activeDir
		}
	}

	pattern := defaultRecordingPattern
	if server.Config != nil && server.Config.Pattern != "" {
		pattern = server.Config.Pattern
	}
	return resolveDefaultRecordingDir(pattern)
}

func resolveDefaultRecordingDir(pattern string) string {
	if pattern == "" {
		pattern = defaultRecordingPattern
	}

	tpl, err := template.New("recording-dir").Parse(pattern)
	if err != nil {
		return "videos"
	}

	var buf bytes.Buffer
	data := struct {
		Username string
		Year     string
		Month    string
		Day      string
		Hour     string
		Minute   string
		Second   string
		Sequence int
	}{
		Username: "username",
		Year:     "2006",
		Month:    "01",
		Day:      "02",
		Hour:     "15",
		Minute:   "04",
		Second:   "05",
		Sequence: 1,
	}
	if err := tpl.Execute(&buf, data); err != nil {
		return "videos"
	}
	return filepath.Dir(buf.String())
}

func buildDiskUsageInfo(path string) *entity.DiskUsageInfo {
	info := &entity.DiskUsageInfo{Path: path}

	statPath := nearestExistingDir(path)
	stats, err := diskUsageStatsFn(statPath)
	if err != nil {
		info.Error = fmt.Sprintf("disk usage %s: %s", statPath, err.Error())
		return info
	}
	totalBytes := stats.totalBytes
	freeBytes := stats.freeBytes
	usedBytes := uint64(0)
	if totalBytes > freeBytes {
		usedBytes = totalBytes - freeBytes
	}

	usedPercent := 0
	if totalBytes > 0 {
		usedPercent = int(float64(usedBytes) / float64(totalBytes) * 100)
	}

	isWarning, warningReason := diskUsageWarning(freeBytes, totalBytes)
	info.TotalBytes = totalBytes
	info.UsedBytes = usedBytes
	info.FreeBytes = freeBytes
	info.UsedPercent = usedPercent
	info.Total = formatDiskBytes(totalBytes)
	info.Used = formatDiskBytes(usedBytes)
	info.Free = formatDiskBytes(freeBytes)
	info.IsWarning = isWarning
	info.WarningReason = warningReason
	return info
}

func nearestExistingDir(path string) string {
	if path == "" {
		return "."
	}

	candidate := filepath.Clean(path)
	for {
		info, err := os.Stat(candidate)
		if err == nil {
			if info.IsDir() {
				return candidate
			}
			return filepath.Dir(candidate)
		}

		parent := filepath.Dir(candidate)
		if parent == candidate {
			return candidate
		}
		candidate = parent
	}
}

func diskUsageWarning(freeBytes, totalBytes uint64) (bool, string) {
	if freeBytes <= diskWarningFreeBytes {
		return true, "20 GB free or less"
	}
	if totalBytes == 0 {
		return false, ""
	}

	leftHi, leftLo := bits.Mul64(freeBytes, 100)
	rightHi, rightLo := bits.Mul64(totalBytes, uint64(diskWarningFreePercent))
	if leftHi < rightHi || (leftHi == rightHi && leftLo <= rightLo) {
		return true, "10% free or less"
	}
	return false, ""
}

func formatDiskBytes(bytes uint64) string {
	maxInt := uint64(int(^uint(0) >> 1))
	if bytes <= maxInt {
		return internal.FormatFilesize(int(bytes))
	}

	const TB = 1024 * 1024 * 1024 * 1024
	return fmt.Sprintf("%.2f TB", float64(bytes)/float64(TB))
}
