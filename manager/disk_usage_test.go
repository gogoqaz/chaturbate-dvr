package manager

import (
	"math"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestDiskUsageWarningThresholdPercent(t *testing.T) {
	isWarning, reason := diskUsageWarning(50*1024*1024*1024, 500*1024*1024*1024)

	if !isWarning {
		t.Fatal("diskUsageWarning() = false, want true")
	}
	if reason != "10% free or less" {
		t.Fatalf("warning reason = %q, want %q", reason, "10% free or less")
	}
}

func TestDiskUsageWarningThresholdPercentHealthyAboveTenPercent(t *testing.T) {
	isWarning, reason := diskUsageWarning(55*1024*1024*1024, 500*1024*1024*1024)

	if isWarning {
		t.Fatalf("diskUsageWarning() = true, want false, reason = %q", reason)
	}
	if reason != "" {
		t.Fatalf("warning reason = %q, want empty", reason)
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

func TestDiskUsageWarningThresholdBytesOnSmallFilesystem(t *testing.T) {
	isWarning, reason := diskUsageWarning(15*1024*1024*1024, 15*1024*1024*1024)

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

func TestDiskUsageWarningThresholdPercentMaxUint64HealthyAboveTenPercent(t *testing.T) {
	isWarning, reason := diskUsageWarning(math.MaxUint64/10+1, math.MaxUint64)

	if isWarning {
		t.Fatalf("diskUsageWarning() = true, want false, reason = %q", reason)
	}
	if reason != "" {
		t.Fatalf("warning reason = %q, want empty", reason)
	}
}

func TestDiskUsageWarningThresholdPercentMaxUint64AtTenPercent(t *testing.T) {
	isWarning, reason := diskUsageWarning(math.MaxUint64/10, math.MaxUint64)

	if !isWarning {
		t.Fatal("diskUsageWarning() = false, want true")
	}
	if reason != "10% free or less" {
		t.Fatalf("warning reason = %q, want %q", reason, "10% free or less")
	}
}

func TestResolveDefaultRecordingDirFromPattern(t *testing.T) {
	dir := resolveDefaultRecordingDir(filepath.Join("videos", "{{.Username}}_{{.Year}}-{{.Month}}-{{.Day}}"))

	if dir != "videos" {
		t.Fatalf("resolveDefaultRecordingDir() = %q, want %q", dir, "videos")
	}
}

func TestResolveDefaultRecordingDirInvalidPatternFallsBackToVideos(t *testing.T) {
	dir := resolveDefaultRecordingDir("{{")

	if dir != "videos" {
		t.Fatalf("resolveDefaultRecordingDir() = %q, want %q", dir, "videos")
	}
}

func TestActiveRecordingDirUsesTrackedDirectory(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	m.SetRecordingDir("alice", filepath.Join("videos", "alice"))
	if got := m.activeRecordingDir(); got != filepath.Join("videos", "alice") {
		t.Fatalf("activeRecordingDir() = %q, want %q", got, filepath.Join("videos", "alice"))
	}

	m.ClearRecordingDir("alice")
	if got := m.activeRecordingDir(); got == filepath.Join("videos", "alice") {
		t.Fatalf("activeRecordingDir() = %q after clear, want fallback path", got)
	}
}

func TestBuildDiskUsageInfoStatfsTotalBytesOverflow(t *testing.T) {
	original := diskStatfs
	diskStatfs = func(_ string, stat *syscall.Statfs_t) error {
		stat.Bsize = 4096
		stat.Blocks = math.MaxUint64/uint64(stat.Bsize) + 1
		stat.Bavail = 1
		return nil
	}
	t.Cleanup(func() {
		diskStatfs = original
	})

	info := buildDiskUsageInfo("overflow/path")
	if info.Error == "" {
		t.Fatal("Error is empty, want overflow text")
	}
	if info.Path != "overflow/path" {
		t.Fatalf("Path = %q, want %q", info.Path, "overflow/path")
	}
	if info.IsWarning {
		t.Fatal("IsWarning = true for overflow disk info, want false")
	}
}

func TestBuildDiskUsageInfoStatfsFreeBytesOverflow(t *testing.T) {
	original := diskStatfs
	diskStatfs = func(_ string, stat *syscall.Statfs_t) error {
		stat.Bsize = 4096
		stat.Blocks = 1
		stat.Bavail = math.MaxUint64/uint64(stat.Bsize) + 1
		return nil
	}
	t.Cleanup(func() {
		diskStatfs = original
	})

	info := buildDiskUsageInfo("overflow/path")
	if info.Error == "" {
		t.Fatal("Error is empty, want overflow text")
	}
	if info.Path != "overflow/path" {
		t.Fatalf("Path = %q, want %q", info.Path, "overflow/path")
	}
	if info.IsWarning {
		t.Fatal("IsWarning = true for overflow disk info, want false")
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

func TestNewDoesNotStartDiskStatusPublisher(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if m.diskStatusCancel != nil {
		t.Fatal("disk status publisher started from New(), want explicit web setup start")
	}
}

func TestDiskStatusPublisherLifecycleIsIdempotent(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	m.StartDiskStatusPublisher(time.Hour)
	firstCtx := m.diskStatusCtx
	if firstCtx == nil {
		t.Fatal("disk status publisher context is nil after start")
	}
	if m.diskStatusCancel == nil {
		t.Fatal("disk status publisher cancel is nil after start")
	}

	m.StartDiskStatusPublisher(time.Hour)
	if m.diskStatusCtx == nil {
		t.Fatal("disk status publisher context is nil after second start")
	}
	if m.diskStatusCancel == nil {
		t.Fatal("disk status publisher cancel is nil after second start")
	}
	if m.diskStatusCtx != firstCtx {
		t.Fatal("StartDiskStatusPublisher replaced the running publisher, want idempotent start")
	}

	m.StopDiskStatusPublisher()
	if m.diskStatusCtx != nil {
		t.Fatal("disk status publisher context is not nil after stop")
	}
	if m.diskStatusCancel != nil {
		t.Fatal("disk status publisher cancel is not nil after stop")
	}

	m.StopDiskStatusPublisher()
	if m.diskStatusCtx != nil {
		t.Fatal("disk status publisher context is not nil after second stop")
	}
	if m.diskStatusCancel != nil {
		t.Fatal("disk status publisher cancel is not nil after second stop")
	}
}

func TestShouldPublishDiskStatusThrottlesRepeatedCalls(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
	if !m.shouldPublishDiskStatus(now) {
		t.Fatal("first shouldPublishDiskStatus() = false, want true")
	}
	if m.shouldPublishDiskStatus(now.Add(time.Second)) {
		t.Fatal("second shouldPublishDiskStatus() inside throttle window = true, want false")
	}
	if !m.shouldPublishDiskStatus(now.Add(5 * time.Second)) {
		t.Fatal("shouldPublishDiskStatus() at throttle boundary = false, want true")
	}
}

func TestPublishDiskStatusSkipsStatfsWithinThrottleWindow(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	original := diskStatfs
	statfsCalls := 0
	diskStatfs = func(_ string, stat *syscall.Statfs_t) error {
		statfsCalls++
		stat.Bsize = 4096
		stat.Blocks = 100
		stat.Bavail = 50
		return nil
	}
	t.Cleanup(func() {
		diskStatfs = original
	})

	m.PublishDiskStatus()
	m.PublishDiskStatus()

	if statfsCalls != 1 {
		t.Fatalf("diskStatfs calls = %d, want 1", statfsCalls)
	}
}
