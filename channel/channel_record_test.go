package channel

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
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

// buildFragmentedMP4 creates a minimal valid fragmented MP4 in memory with one track and one sample.
func buildFragmentedMP4(t *testing.T, mediaType string, timescale uint32, sampleData []byte) []byte {
	t.Helper()

	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(timescale, mediaType, "und")
	if err := init.TweakSingleTrakLive(); err != nil {
		t.Fatalf("TweakSingleTrakLive(%s) error = %v", mediaType, err)
	}

	seg := mp4.NewMediaSegmentWithoutStyp()
	frag, err := mp4.CreateFragment(1, 1)
	if err != nil {
		t.Fatalf("CreateFragment(%s) error = %v", mediaType, err)
	}
	if err := frag.AddFullSampleToTrack(mp4.FullSample{
		Sample:     mp4.Sample{Flags: mp4.SyncSampleFlags, Dur: timescale, Size: uint32(len(sampleData))},
		DecodeTime: 0,
		Data:       sampleData,
	}, 1); err != nil {
		t.Fatalf("AddFullSampleToTrack(%s) error = %v", mediaType, err)
	}
	seg.AddFragment(frag)

	var buf bytes.Buffer
	if err := init.Encode(&buf); err != nil {
		t.Fatalf("encode init(%s) error = %v", mediaType, err)
	}
	if err := seg.Encode(&buf); err != nil {
		t.Fatalf("encode segment(%s) error = %v", mediaType, err)
	}
	return buf.Bytes()
}

func TestCleanupNativeMuxesSeparateTracksWhenFFmpegUnavailable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	videoMP4 := buildFragmentedMP4(t, "video", 90000, []byte{0x00, 0x00, 0x00, 0x01, 0x67}) // fake NAL unit
	audioMP4 := buildFragmentedMP4(t, "audio", 44100, []byte{0xFF, 0xF1})                    // fake AAC frame

	base := filepath.Join(dir, "recording")
	ch := New(&entity.ChannelConfig{
		Username: "alice",
		Pattern:  base,
	})
	ch.HasSeparateAudio = true
	ch.CurrentFilename = base
	ch.InitSegment = videoMP4
	ch.AudioInitSegment = audioMP4

	if err := ch.CreateNewFile(base); err != nil {
		t.Fatalf("CreateNewFile() error = %v", err)
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
