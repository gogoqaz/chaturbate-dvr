package channel

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/teacat/chaturbate-dvr/entity"
)

const defaultPattern = "{{.Username}}_{{.Year}}-{{.Month}}-{{.Day}}_{{.Hour}}-{{.Minute}}-{{.Second}}{{if .Sequence}}_{{.Sequence}}{{end}}"

func TestRemuxOrphansMergesLeftoverSidecars(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir) // no ffmpeg: exercises the native muxer

	base := filepath.Join(dir, "alice_2025-09-03_10-00-00")
	writeStaleSidecars(t, base,
		buildFragmentedMP4(t, "video", 90000, []byte{0x00, 0x00, 0x00, 0x01, 0x67}),
		buildFragmentedMP4(t, "audio", 44100, []byte{0xFF, 0xF1}))

	ch := New(&entity.ChannelConfig{
		Username: "alice",
		Pattern:  filepath.Join(dir, defaultPattern),
	})

	merged, err := ch.RemuxOrphans()
	if err != nil {
		t.Fatalf("RemuxOrphans() error = %v", err)
	}
	if merged != 1 {
		t.Fatalf("merged = %d, want 1", merged)
	}

	muxed, err := mp4.ReadMP4File(base + ".mp4")
	if err != nil {
		t.Fatalf("ReadMP4File() error = %v", err)
	}
	if len(muxed.Init.Moov.Traks) != 2 {
		t.Fatalf("tracks in remuxed output = %d, want 2", len(muxed.Init.Moov.Traks))
	}
	if _, err := os.Stat(base + videoSidecarSuffix); !os.IsNotExist(err) {
		t.Fatalf("expected video sidecar removed, stat err = %v", err)
	}
	if _, err := os.Stat(base + audioSidecarSuffix); !os.IsNotExist(err) {
		t.Fatalf("expected audio sidecar removed, stat err = %v", err)
	}
}

func TestFindOrphanPairsSkipsOtherChannelsRecordings(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mine := filepath.Join(dir, "alice_2025-09-03_10-00-00")
	theirs := filepath.Join(dir, "alicia_2025-09-03_10-00-00")
	writeStaleSidecars(t, mine, []byte("video"), []byte("audio"))
	writeStaleSidecars(t, theirs, []byte("video"), []byte("audio"))

	ch := New(&entity.ChannelConfig{
		Username: "alice",
		Pattern:  filepath.Join(dir, defaultPattern),
	})

	bases, err := ch.findOrphanPairs()
	if err != nil {
		t.Fatalf("findOrphanPairs() error = %v", err)
	}
	if len(bases) != 1 || bases[0] != mine {
		t.Fatalf("bases = %v, want [%s]", bases, mine)
	}
}

func TestFindOrphanPairsMatchesRotatedAndNestedRecordings(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	pattern := filepath.Join(dir, "{{.Year}}", "{{.Month}}", defaultPattern)
	nested := filepath.Join(dir, "2025", "09", "alice_2025-09-03_10-00-00_4")
	if err := os.MkdirAll(filepath.Dir(nested), 0o777); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeStaleSidecars(t, nested, []byte("video"), []byte("audio"))

	ch := New(&entity.ChannelConfig{Username: "alice", Pattern: pattern})

	bases, err := ch.findOrphanPairs()
	if err != nil {
		t.Fatalf("findOrphanPairs() error = %v", err)
	}
	if len(bases) != 1 || bases[0] != nested {
		t.Fatalf("bases = %v, want [%s]", bases, nested)
	}
}

func TestFindOrphanPairsSkipsUnfinishedAndAlreadyMerged(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ch := New(&entity.ChannelConfig{
		Username: "alice",
		Pattern:  filepath.Join(dir, defaultPattern),
	})

	// Being written right now.
	fresh := filepath.Join(dir, "alice_2025-09-03_10-00-00")
	writeSidecar(t, fresh+videoSidecarSuffix, []byte("video"))
	writeSidecar(t, fresh+audioSidecarSuffix, []byte("audio"))

	// Backdated, so only the CurrentFilename guard can keep it out.
	current := filepath.Join(dir, "alice_2025-09-03_11-00-00")
	writeStaleSidecars(t, current, []byte("video"), []byte("audio"))
	ch.CurrentFilename = current

	// Already merged in an earlier run.
	merged := filepath.Join(dir, "alice_2025-09-03_12-00-00")
	writeStaleSidecars(t, merged, []byte("video"), []byte("audio"))
	writeSidecar(t, merged+".mp4", []byte("merged"))

	// Single-track recording: no audio to merge with.
	lone := filepath.Join(dir, "alice_2025-09-03_13-00-00")
	writeSidecar(t, lone+videoSidecarSuffix, []byte("video"))

	bases, err := ch.findOrphanPairs()
	if err != nil {
		t.Fatalf("findOrphanPairs() error = %v", err)
	}
	if len(bases) != 0 {
		t.Fatalf("bases = %v, want none", bases)
	}
}

func TestRemuxOrphansKeepsSidecarsWhenMergeFails(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir) // no ffmpeg, and the payload is not a valid fMP4

	base := filepath.Join(dir, "alice_2025-09-03_10-00-00")
	writeStaleSidecars(t, base, []byte("not-an-mp4"), []byte("not-an-mp4"))

	ch := New(&entity.ChannelConfig{
		Username: "alice",
		Pattern:  filepath.Join(dir, defaultPattern),
	})

	merged, err := ch.RemuxOrphans()
	if err != nil {
		t.Fatalf("RemuxOrphans() error = %v", err)
	}
	if merged != 0 {
		t.Fatalf("merged = %d, want 0", merged)
	}
	for _, suffix := range []string{videoSidecarSuffix, audioSidecarSuffix} {
		if _, err := os.Stat(base + suffix); err != nil {
			t.Fatalf("sidecar %s must survive a failed merge, stat err = %v", suffix, err)
		}
	}
}

func TestFinalizeMuxRefusesToMergeAnOutputAnotherMergeOwns(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	base := filepath.Join(dir, "alice_2025-09-03_10-00-00")
	writeStaleSidecars(t, base, []byte("video"), []byte("audio"))
	output := base + ".mp4"

	// Stand in for a rotation's Cleanup that is already muxing this file.
	inFlightMux.Store(output, struct{}{})
	defer inFlightMux.Delete(output)

	ch := New(&entity.ChannelConfig{
		Username: "alice",
		Pattern:  filepath.Join(dir, defaultPattern),
	})

	videoInfo, err := os.Stat(base + videoSidecarSuffix)
	if err != nil {
		t.Fatalf("stat video sidecar: %v", err)
	}
	audioInfo, err := os.Stat(base + audioSidecarSuffix)
	if err != nil {
		t.Fatalf("stat audio sidecar: %v", err)
	}

	if err := ch.FinalizeMux(base+videoSidecarSuffix, base+audioSidecarSuffix, output, videoInfo, audioInfo); !errors.Is(err, ErrMuxBusy) {
		t.Fatalf("FinalizeMux() error = %v, want ErrMuxBusy", err)
	}
	for _, suffix := range []string{videoSidecarSuffix, audioSidecarSuffix} {
		if _, err := os.Stat(base + suffix); err != nil {
			t.Fatalf("sidecar %s must survive a skipped merge, stat err = %v", suffix, err)
		}
	}
}

func TestPatternRoot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern string
		want    string
	}{
		{"videos/{{.Username}}_{{.Year}}", filepath.Clean("videos")},
		{"videos/{{.Year}}/{{.Month}}/{{.Username}}", filepath.Clean("videos")},
		{"videos/rec/{{.Username}}", filepath.Clean("videos/rec")},
		{"{{.Username}}", "."},
		{"recording", "."},
	}
	for _, tt := range tests {
		if got := patternRoot(tt.pattern); got != tt.want {
			t.Errorf("patternRoot(%q) = %q, want %q", tt.pattern, got, tt.want)
		}
	}
}

func TestWildcardMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"alice_*-*-*_*-*-*", "alice_2025-09-03_10-00-00", true},
		{"alice_*-*-*_*-*-*", "alicia_2025-09-03_10-00-00", false},
		{"alice_*-*-*_*-*-*_*", "alice_2025-09-03_10-00-00_4", true},
		// A wildcard spans anything but a separator, so the sequence-less
		// variant covers rotated files too. Still alice's, which is the point.
		{"alice_*-*-*_*-*-*", "alice_2025-09-03_10-00-00_4", true},
		{"videos/*/alice", "videos/2025/alice", true},
		{"videos/*/alice", "videos/2025/09/alice", false},
		{"videos/*alice", "videos/2025/alice", false},
		{"*", "anything", true},
	}
	for _, tt := range tests {
		if got := wildcardMatch(tt.pattern, tt.name); got != tt.want {
			t.Errorf("wildcardMatch(%q, %q) = %v, want %v", tt.pattern, tt.name, got, tt.want)
		}
	}
}

func writeSidecar(t *testing.T, path string, data []byte) {
	t.Helper()

	if err := os.WriteFile(path, data, 0o666); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// writeStaleSidecars backdates the pair past the quiet period, so a scan sees
// a finished recording rather than a live one.
func writeStaleSidecars(t *testing.T, base string, video, audio []byte) {
	t.Helper()

	stale := time.Now().Add(-remuxQuietPeriod - time.Minute)
	for path, data := range map[string][]byte{
		base + videoSidecarSuffix: video,
		base + audioSidecarSuffix: audio,
	} {
		writeSidecar(t, path, data)
		if err := os.Chtimes(path, stale, stale); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}
}
