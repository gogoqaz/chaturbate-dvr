package channel

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/teacat/chaturbate-dvr/entity"
	"github.com/teacat/chaturbate-dvr/server"
)

type noopManager struct{}

func (noopManager) CreateChannel(*entity.ChannelConfig, bool) error { return nil }
func (noopManager) StopChannel(string) error                        { return nil }
func (noopManager) PauseChannel(string) error                       { return nil }
func (noopManager) ResumeChannel(string) error                      { return nil }
func (noopManager) ChannelInfo() []*entity.ChannelInfo              { return nil }
func (noopManager) Publish(string, *entity.ChannelInfo)             {}
func (noopManager) Subscriber(http.ResponseWriter, *http.Request)   {}
func (noopManager) LoadConfig() error                               { return nil }
func (noopManager) SaveConfig() error                               { return nil }

func init() {
	server.Manager = noopManager{}
}

func TestHandleInitSegmentRenamesFileToMP4(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ch := New(&entity.ChannelConfig{
		Username: "alice",
		Pattern:  filepath.Join(dir, "recording"),
	})
	ch.StreamedAt = 1

	if err := ch.CreateNewFile(filepath.Join(dir, "recording")); err != nil {
		t.Fatalf("CreateNewFile() error = %v", err)
	}

	initSegment := []byte("ftyp-test-moov")
	if err := ch.HandleInitSegment(initSegment); err != nil {
		t.Fatalf("HandleInitSegment() error = %v", err)
	}
	t.Cleanup(func() { _ = ch.Cleanup() })

	if _, err := os.Stat(filepath.Join(dir, "recording.ts")); !os.IsNotExist(err) {
		t.Fatalf("expected .ts file to be renamed, stat err = %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "recording.mp4"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != string(initSegment) {
		t.Fatalf("mp4 contents = %q, want %q", string(got), string(initSegment))
	}
}

func TestCreateNewFileWritesInitSegmentForRotatedFMP4Files(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	initSegment := []byte("ftyp-test-moov")
	ch := New(&entity.ChannelConfig{
		Username: "alice",
		Pattern:  filepath.Join(dir, "recording"),
	})
	ch.InitSegment = initSegment

	if err := ch.CreateNewFile(filepath.Join(dir, "rotated")); err != nil {
		t.Fatalf("CreateNewFile() error = %v", err)
	}
	t.Cleanup(func() { _ = ch.Cleanup() })

	got, err := os.ReadFile(filepath.Join(dir, "rotated.mp4"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != string(initSegment) {
		t.Fatalf("mp4 contents = %q, want %q", string(got), string(initSegment))
	}
}

func TestCleanupNativeMuxesSeparateTracksWhenFFmpegUnavailable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	base := filepath.Join(dir, "recording")
	ch := New(&entity.ChannelConfig{
		Username: "alice",
		Pattern:  base,
	})
	ch.HasSeparateAudio = true
	ch.CurrentFilename = base
	ch.InitSegment = []byte("video-init")
	ch.AudioInitSegment = []byte("audio-init")

	if err := ch.CreateNewFile(base); err != nil {
		t.Fatalf("CreateNewFile() error = %v", err)
	}

	if _, err := ch.File.Write([]byte("video-fragment")); err != nil {
		t.Fatalf("write video fragment error = %v", err)
	}
	if _, err := ch.AudioFile.Write([]byte("audio-fragment")); err != nil {
		t.Fatalf("write audio fragment error = %v", err)
	}

	if err := ch.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	info, err := os.Stat(base + ".mp4")
	if err != nil {
		t.Fatalf("expected final mp4, stat err = %v", err)
	}
	if info.Size() <= 0 {
		t.Fatalf("expected final mp4 to be non-empty, size = %d", info.Size())
	}
	if _, err := os.Stat(base + ".video.mp4"); !os.IsNotExist(err) {
		t.Fatalf("expected video sidecar removed, stat err = %v", err)
	}
	if _, err := os.Stat(base + ".audio.mp4"); !os.IsNotExist(err) {
		t.Fatalf("expected audio sidecar removed, stat err = %v", err)
	}
}

func TestCreateNewFileKeepsLegacyHLSAsTS(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	base := filepath.Join(dir, "legacy")
	ch := New(&entity.ChannelConfig{
		Username: "alice",
		Pattern:  base,
	})

	if err := ch.CreateNewFile(base); err != nil {
		t.Fatalf("CreateNewFile() error = %v", err)
	}
	t.Cleanup(func() { _ = ch.Cleanup() })

	if _, err := os.Stat(base + ".ts"); err != nil {
		t.Fatalf("expected legacy ts output, stat err = %v", err)
	}
	if _, err := os.Stat(base + ".mp4"); !os.IsNotExist(err) {
		t.Fatalf("expected legacy mp4 output to not exist, stat err = %v", err)
	}
}
