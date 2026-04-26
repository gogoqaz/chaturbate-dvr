package manager

import (
	"errors"
	"math"
	"os"
	"path/filepath"
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

func TestBuildDiskUsageInfoDiskStatsFailure(t *testing.T) {
	original := diskUsageStatsFn
	diskUsageStatsFn = func(string) (diskUsageStats, error) {
		return diskUsageStats{}, errors.New("stat failed")
	}
	t.Cleanup(func() {
		diskUsageStatsFn = original
	})

	info := buildDiskUsageInfo("missing/path")
	if info.Error == "" {
		t.Fatal("Error is empty, want disk stats failure text")
	}
	if info.Path != "missing/path" {
		t.Fatalf("Path = %q, want %q", info.Path, "missing/path")
	}
	if info.IsWarning {
		t.Fatal("IsWarning = true for unavailable disk info, want false")
	}
}

func TestBuildDiskUsageInfoIncludesFolderSize(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "one.ts"), make([]byte, 1024), 0666); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0777); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "two.ts"), make([]byte, 2048), 0666); err != nil {
		t.Fatalf("WriteFile() nested error = %v", err)
	}

	original := diskUsageStatsFn
	diskUsageStatsFn = func(string) (diskUsageStats, error) {
		return diskUsageStats{totalBytes: 100 * 1024, freeBytes: 50 * 1024}, nil
	}
	t.Cleanup(func() {
		diskUsageStatsFn = original
	})

	info := buildDiskUsageInfo(dir)
	if info.FolderSizeError != "" {
		t.Fatalf("FolderSizeError = %q, want empty", info.FolderSizeError)
	}
	if info.FolderSizeBytes != 3072 {
		t.Fatalf("FolderSizeBytes = %d, want 3072", info.FolderSizeBytes)
	}
	if info.FolderSize != "3.00 KB" {
		t.Fatalf("FolderSize = %q, want %q", info.FolderSize, "3.00 KB")
	}
}

func TestBuildDiskUsageInfoFolderSizeUnavailable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")

	original := diskUsageStatsFn
	diskUsageStatsFn = func(string) (diskUsageStats, error) {
		return diskUsageStats{totalBytes: 100 * 1024, freeBytes: 50 * 1024}, nil
	}
	t.Cleanup(func() {
		diskUsageStatsFn = original
	})

	info := buildDiskUsageInfo(dir)
	if info.Error != "" {
		t.Fatalf("Error = %q, want empty disk usage error", info.Error)
	}
	if info.FolderSizeError == "" {
		t.Fatal("FolderSizeError is empty, want missing folder error")
	}
	if info.FolderSize != "" {
		t.Fatalf("FolderSize = %q, want empty", info.FolderSize)
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

	original := diskUsageStatsFn
	statsCalls := 0
	diskUsageStatsFn = func(string) (diskUsageStats, error) {
		statsCalls++
		return diskUsageStats{
			totalBytes: 409600,
			freeBytes:  204800,
		}, nil
	}
	t.Cleanup(func() {
		diskUsageStatsFn = original
	})

	m.PublishDiskStatus()
	m.PublishDiskStatus()

	if statsCalls != 1 {
		t.Fatalf("disk usage stats calls = %d, want 1", statsCalls)
	}
}
