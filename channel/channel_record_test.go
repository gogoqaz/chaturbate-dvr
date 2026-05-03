package channel

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	return buildFragmentedMP4WithSamples(t, mediaType, timescale, []byte(sampleData), 1)
}

// buildFragmentedMP4WithSamples creates a fragmented MP4 with the requested
// number of single-sample fragments, each holding sampleData and lasting one
// second of media (Dur = timescale per sample).
func buildFragmentedMP4WithSamples(t *testing.T, mediaType string, timescale uint32, sampleData []byte, fragments int) []byte {
	t.Helper()

	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(timescale, mediaType, "und")
	if err := init.TweakSingleTrakLive(); err != nil {
		t.Fatalf("TweakSingleTrakLive(%s) error = %v", mediaType, err)
	}

	var buf bytes.Buffer
	if err := init.Encode(&buf); err != nil {
		t.Fatalf("encode init(%s) error = %v", mediaType, err)
	}

	for i := 0; i < fragments; i++ {
		seg := mp4.NewMediaSegmentWithoutStyp()
		frag, err := mp4.CreateFragment(uint32(i+1), 1)
		if err != nil {
			t.Fatalf("CreateFragment(%s) error = %v", mediaType, err)
		}
		if err := frag.AddFullSampleToTrack(mp4.FullSample{
			Sample:     mp4.Sample{Flags: mp4.SyncSampleFlags, Dur: timescale, Size: uint32(len(sampleData))},
			DecodeTime: uint64(i) * uint64(timescale),
			Data:       sampleData,
		}, 1); err != nil {
			t.Fatalf("AddFullSampleToTrack(%s) error = %v", mediaType, err)
		}
		seg.AddFragment(frag)
		if err := seg.Encode(&buf); err != nil {
			t.Fatalf("encode segment(%s) error = %v", mediaType, err)
		}
	}
	return buf.Bytes()
}

func TestCleanupNativeMuxesSeparateTracksWhenFFmpegUnavailable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	videoMP4 := buildFragmentedMP4(t, "video", 90000, []byte{0x00, 0x00, 0x00, 0x01, 0x67}) // fake NAL unit
	audioMP4 := buildFragmentedMP4(t, "audio", 44100, []byte{0xFF, 0xF1})                   // fake AAC frame

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

	outputPath := base + ".mp4"
	info, err := os.Stat(outputPath)
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

	// Verify the muxed output contains both video and audio tracks
	muxed, err := mp4.ReadMP4File(outputPath)
	if err != nil {
		t.Fatalf("ReadMP4File() error = %v", err)
	}
	if len(muxed.Init.Moov.Traks) != 2 {
		t.Fatalf("expected 2 tracks in muxed output, got %d", len(muxed.Init.Moov.Traks))
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

func TestHandleSegmentDefersRotationForSeparateAudio(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	pattern := filepath.Join(dir, "rotating{{if .Sequence}}_{{.Sequence}}{{end}}")
	ch := New(&entity.ChannelConfig{
		Username:    "alice",
		Pattern:     pattern,
		MaxFilesize: 1, // 1 MiB threshold
	})
	ch.StreamedAt = 1
	ch.HasSeparateAudio = true

	if err := ch.NextFile(); err != nil {
		t.Fatalf("NextFile() error = %v", err)
	}
	t.Cleanup(func() { _ = ch.Cleanup() })

	firstName := ch.File.Name()

	// Trigger ShouldSwitchFile by writing past MaxFilesize.
	if err := ch.HandleSegment(make([]byte, 2*1024*1024), 1); err != nil {
		t.Fatalf("HandleSegment() error = %v", err)
	}

	if !ch.switchRequested {
		t.Fatalf("switchRequested = false after oversized write, want true")
	}
	if ch.File.Name() != firstName {
		t.Fatalf("file rotated inside HandleSegment: %q -> %q", firstName, ch.File.Name())
	}

	if err := ch.OnPollComplete(); err != nil {
		t.Fatalf("OnPollComplete() error = %v", err)
	}
	if ch.switchRequested {
		t.Fatalf("switchRequested still set after OnPollComplete")
	}
	if ch.File.Name() == firstName {
		t.Fatalf("file not rotated after OnPollComplete (still %q)", firstName)
	}
}

func TestHandleSegmentRotatesImmediatelyForSingleStream(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	pattern := filepath.Join(dir, "single{{if .Sequence}}_{{.Sequence}}{{end}}")
	ch := New(&entity.ChannelConfig{
		Username:    "alice",
		Pattern:     pattern,
		MaxFilesize: 1, // 1 MiB threshold
	})
	ch.StreamedAt = 1
	// HasSeparateAudio stays false: no audio playlist, no pairing risk.

	if err := ch.NextFile(); err != nil {
		t.Fatalf("NextFile() error = %v", err)
	}
	t.Cleanup(func() { _ = ch.Cleanup() })

	firstName := ch.File.Name()

	if err := ch.HandleSegment(make([]byte, 2*1024*1024), 1); err != nil {
		t.Fatalf("HandleSegment() error = %v", err)
	}

	if ch.switchRequested {
		t.Fatalf("switchRequested = true for single-stream recording, want false")
	}
	if ch.File.Name() == firstName {
		t.Fatalf("file not rotated immediately for single-stream recording (still %q)", firstName)
	}
}

func TestOnPollCompleteNoopWhenNothingRequested(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ch := New(&entity.ChannelConfig{
		Username: "alice",
		Pattern:  filepath.Join(dir, "x"),
	})

	if err := ch.OnPollComplete(); err != nil {
		t.Fatalf("OnPollComplete() with no flag error = %v", err)
	}
}

func TestCleanupPreservesAudioOnlyWhenVideoMissing(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "recording")
	audioPath := base + ".audio.mp4"
	audioFile, err := os.OpenFile(audioPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open audio file: %v", err)
	}
	if _, err := audioFile.Write([]byte("audio-payload")); err != nil {
		t.Fatalf("write audio file: %v", err)
	}

	ch := New(&entity.ChannelConfig{Username: "alice", Pattern: base})
	ch.HasSeparateAudio = true
	ch.CurrentFilename = base
	ch.AudioFile = audioFile

	if err := ch.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Stat(audioPath); err != nil {
		t.Fatalf("audio file should be preserved, stat err = %v", err)
	}
}

func TestCleanupPreservesVideoOnlyWhenAudioMissing(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "recording")
	videoPath := base + ".video.mp4"
	videoFile, err := os.OpenFile(videoPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open video file: %v", err)
	}
	if _, err := videoFile.Write([]byte("video-payload")); err != nil {
		t.Fatalf("write video file: %v", err)
	}

	ch := New(&entity.ChannelConfig{Username: "alice", Pattern: base})
	ch.HasSeparateAudio = true
	ch.CurrentFilename = base
	ch.File = videoFile

	if err := ch.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Stat(videoPath); err != nil {
		t.Fatalf("video file should be preserved, stat err = %v", err)
	}
}

func TestMuxOutputLooksValid(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	writeSized := func(name string, size int) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, make([]byte, size), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}

	videoPath := writeSized("v", 1000)
	audioPath := writeSized("a", 200)
	videoInfo, _ := os.Stat(videoPath)
	audioInfo, _ := os.Stat(audioPath)

	okOutput := writeSized("ok.mp4", 900) // 900 >= (1200 / 2)
	tinyOutput := writeSized("tiny.mp4", 100)
	emptyOutput := writeSized("empty.mp4", 0)

	if ok, reason := muxOutputLooksValid(okOutput, videoInfo, audioInfo); !ok {
		t.Fatalf("expected valid, got reason %q", reason)
	}
	if ok, _ := muxOutputLooksValid(tinyOutput, videoInfo, audioInfo); ok {
		t.Fatalf("expected invalid for tiny output")
	}
	if ok, _ := muxOutputLooksValid(emptyOutput, videoInfo, audioInfo); ok {
		t.Fatalf("expected invalid for empty output")
	}
	if ok, _ := muxOutputLooksValid(filepath.Join(dir, "missing.mp4"), videoInfo, audioInfo); ok {
		t.Fatalf("expected invalid for missing output")
	}
}

func TestCompressFilePreservesInputTiming(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "ffmpeg.log")
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
last=""
for arg in "$@"; do
  last="$arg"
done
case "$last" in
  *.mkv) printf 'compressed' > "$last" ;;
esac
`, logPath)
	if err := os.WriteFile(ffmpegPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	// Stub ffprobe to report aligned streams so this test only checks the
	// timing-preservation flags. Offset trimming is exercised separately.
	ffprobePath := filepath.Join(dir, "ffprobe")
	if err := os.WriteFile(ffprobePath, []byte("#!/bin/sh\necho 0.000000\n"), 0755); err != nil {
		t.Fatalf("write fake ffprobe: %v", err)
	}
	t.Setenv("PATH", dir)

	detectedEncoder = ""
	detectedEncoderOnce = sync.Once{}
	fpsPassthroughFlag = nil
	fpsPassthroughOnce = sync.Once{}

	srcPath := filepath.Join(dir, "recording.mp4")
	if err := os.WriteFile(srcPath, []byte("source-video"), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	ch := New(&entity.ChannelConfig{Username: "alice", Pattern: filepath.Join(dir, "recording")})
	ch.CompressFile(srcPath)

	var log string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(logPath)
		if err == nil {
			log = string(data)
			if strings.Contains(log, ".mkv") {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(log, ".mkv") {
		t.Fatalf("compress ffmpeg command did not run, log = %q", log)
	}

	lines := strings.Split(strings.TrimSpace(log), "\n")
	compressArgs := lines[len(lines)-1]
	for _, want := range []string{"-copyts", "-start_at_zero"} {
		if !strings.Contains(compressArgs, want) {
			t.Fatalf("compress args = %q, want %q to preserve input timing", compressArgs, want)
		}
	}
	// Accept either modern (-fps_mode passthrough) or legacy (-vsync
	// passthrough) frame-timing flag, since the chosen one depends on the
	// installed ffmpeg version.
	if !strings.Contains(compressArgs, "-fps_mode passthrough") &&
		!strings.Contains(compressArgs, "-vsync passthrough") {
		t.Fatalf("compress args = %q, want -fps_mode or -vsync passthrough", compressArgs)
	}
	// Aligned streams (ffprobe stub returns 0) must not insert -ss.
	if strings.Contains(compressArgs, "-ss ") {
		t.Fatalf("compress args = %q, did not expect -ss when streams are aligned", compressArgs)
	}
}

func TestBuildCompressArgsAddsLeadingTrim(t *testing.T) {
	t.Parallel()

	enc := videoEncoder{name: "CPU", codec: "libx264", args: []string{"-preset", "medium", "-crf", "23"}}
	fps := []string{"-fps_mode", "passthrough"}

	aligned := buildCompressArgs("/in.mp4", "/out.mkv", enc, fps, 0)
	if containsArg(aligned, "-ss") {
		t.Fatalf("aligned compress args contain -ss: %v", aligned)
	}

	misaligned := buildCompressArgs("/in.mp4", "/out.mkv", enc, fps, 1.246)
	idx := indexOfArg(misaligned, "-ss")
	if idx < 0 {
		t.Fatalf("misaligned compress args missing -ss: %v", misaligned)
	}
	if got := misaligned[idx+1]; got != "1.246" {
		t.Fatalf("-ss value = %q, want 1.246", got)
	}
	// -ss must precede -i so it applies as input-side seek.
	if iIdx := indexOfArg(misaligned, "-i"); iIdx <= idx {
		t.Fatalf("-ss (%d) must come before -i (%d): %v", idx, iIdx, misaligned)
	}

	// Sub-threshold offsets are ignored to avoid trimming sub-frame jitter.
	jitter := buildCompressArgs("/in.mp4", "/out.mkv", enc, fps, 0.02)
	if containsArg(jitter, "-ss") {
		t.Fatalf("sub-threshold offset triggered -ss: %v", jitter)
	}
}

func TestDetectStreamStartOffsetSecWithFakeFFprobe(t *testing.T) {
	dir := t.TempDir()
	ffprobe := filepath.Join(dir, "ffprobe")
	// Fake ffprobe replies with the value matching the requested stream.
	script := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    "v:0") want=v ;;
    "a:0") want=a ;;
  esac
done
case "$want" in
  v) echo 0.000000 ;;
  a) echo 1.246000 ;;
esac
`
	if err := os.WriteFile(ffprobe, []byte(script), 0755); err != nil {
		t.Fatalf("write fake ffprobe: %v", err)
	}
	t.Setenv("PATH", dir)

	got := detectStreamStartOffsetSec(filepath.Join(dir, "irrelevant.mp4"))
	if got < 1.245 || got > 1.247 {
		t.Fatalf("detected offset = %v, want ~1.246", got)
	}

	// Probe failure (ffprobe missing) returns 0 without panicking.
	t.Setenv("PATH", t.TempDir())
	if got := detectStreamStartOffsetSec("/nope.mp4"); got != 0 {
		t.Fatalf("expected 0 when ffprobe missing, got %v", got)
	}
}

func containsArg(args []string, target string) bool {
	return indexOfArg(args, target) >= 0
}

func indexOfArg(args []string, target string) int {
	for i, a := range args {
		if a == target {
			return i
		}
	}
	return -1
}

func TestNativeMuxWritesNonZeroDuration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	const fragments = 5
	videoMP4 := buildFragmentedMP4WithSamples(t, "video", 90000, []byte{0x00, 0x00, 0x00, 0x01, 0x67}, fragments)
	audioMP4 := buildFragmentedMP4WithSamples(t, "audio", 44100, []byte{0xFF, 0xF1}, fragments)

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

	muxed, err := mp4.ReadMP4File(base + ".mp4")
	if err != nil {
		t.Fatalf("ReadMP4File() error = %v", err)
	}
	mvhd := muxed.Init.Moov.Mvhd
	if mvhd.Duration == 0 {
		t.Fatalf("expected mvhd.Duration > 0 to advertise recorded length, got 0 (timescale=%d)", mvhd.Timescale)
	}
	gotSeconds := float64(mvhd.Duration) / float64(mvhd.Timescale)
	if gotSeconds < float64(fragments)-0.5 || gotSeconds > float64(fragments)+0.5 {
		t.Fatalf("mvhd reports %.2fs, want ~%ds", gotSeconds, fragments)
	}

	for _, trak := range muxed.Init.Moov.Traks {
		if trak.Tkhd.Duration == 0 {
			t.Fatalf("track %d tkhd.Duration is zero", trak.Tkhd.TrackID)
		}
		if trak.Mdia == nil || trak.Mdia.Mdhd == nil {
			t.Fatalf("track %d missing mdhd", trak.Tkhd.TrackID)
		}
		mdhd := trak.Mdia.Mdhd
		if mdhd.Duration == 0 {
			t.Fatalf("track %d mdhd.Duration is zero", trak.Tkhd.TrackID)
		}
		mediaSeconds := float64(mdhd.Duration) / float64(mdhd.Timescale)
		if mediaSeconds < float64(fragments)-0.5 || mediaSeconds > float64(fragments)+0.5 {
			t.Fatalf("track %d mdhd reports %.2fs, want ~%ds", trak.Tkhd.TrackID, mediaSeconds, fragments)
		}
	}
}
