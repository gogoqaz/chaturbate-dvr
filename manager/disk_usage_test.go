package manager

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestDiskUsageWarningThresholdPercent(t *testing.T) {
	isWarning, reason := diskUsageWarning(11, 100)

	if !isWarning {
		t.Fatal("diskUsageWarning() = false, want true")
	}
	if reason != "10% free or less" {
		t.Fatalf("warning reason = %q, want %q", reason, "10% free or less")
	}
}

func TestDiskUsageWarningThresholdBytes(t *testing.T) {
	isWarning, reason := diskUsageWarning(20*1024*1024*1024, 500*1024*1024*1024)

	if !isWarning {
		t.Fatal("diskUsageWarning() = false, want true")
	}
	if reason != "20 GB free or less" {
		t.Fatalf("warning reason = %q, want %q", reason, "20 GB free or less")
	}
}

func TestDiskUsageWarningThresholdHealthy(t *testing.T) {
	isWarning, reason := diskUsageWarning(200*1024*1024*1024, 500*1024*1024*1024)

	if isWarning {
		t.Fatalf("diskUsageWarning() = true, want false, reason = %q", reason)
	}
	if reason != "" {
		t.Fatalf("warning reason = %q, want empty", reason)
	}
}

func TestResolveDefaultRecordingDirFromPattern(t *testing.T) {
	dir := resolveDefaultRecordingDir(filepath.Join("videos", "{{.Username}}_{{.Year}}-{{.Month}}-{{.Day}}"))

	if dir != "videos" {
		t.Fatalf("resolveDefaultRecordingDir() = %q, want %q", dir, "videos")
	}
}

func TestBuildDiskUsageInfoStatfsFailure(t *testing.T) {
	original := diskStatfs
	diskStatfs = func(string, *syscall.Statfs_t) error {
		return syscall.ENOENT
	}
	t.Cleanup(func() {
		diskStatfs = original
	})

	info := buildDiskUsageInfo("missing/path")
	if info.Error == "" {
		t.Fatal("Error is empty, want Statfs failure text")
	}
	if info.Path != "missing/path" {
		t.Fatalf("Path = %q, want %q", info.Path, "missing/path")
	}
	if info.IsWarning {
		t.Fatal("IsWarning = true for unavailable disk info, want false")
	}
}
