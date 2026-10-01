package channel

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/teacat/chaturbate-dvr/entity"
)

func TestNextFileAvoidsExistingRecordingFiles(t *testing.T) {
	for _, suffix := range []string{".ts", ".mp4", ".mkv", ".video.ts", ".audio.ts", videoSidecarSuffix, audioSidecarSuffix, ".video.mkv", ".audio.mkv"} {
		t.Run(suffix, func(t *testing.T) {
			base := filepath.Join(t.TempDir(), "alice")
			old := base + suffix
			writeSidecar(t, old, []byte("old recording"))
			ch := bufferedTestChannel(&entity.ChannelConfig{Username: "alice", Pattern: base})
			if err := ch.NextFile(); err != nil {
				t.Fatal(err)
			}
			defer ch.Cleanup()
			if got := ch.currentFilename(); got != base+" (1)" {
				t.Fatalf("base = %q, want collision suffix", got)
			}
			data, err := os.ReadFile(old)
			if err != nil || string(data) != "old recording" {
				t.Fatalf("existing recording was modified: %q %v", data, err)
			}
		})
	}
}

func TestNextFileAllocatesDistinctBasesAcrossChannels(t *testing.T) {
	base := filepath.Join(t.TempDir(), "shared")
	channels := make([]*Channel, 8)
	var wg sync.WaitGroup
	for i := range channels {
		ch := bufferedTestChannel(&entity.ChannelConfig{Username: "alice", Pattern: base})
		ch.HasSeparateAudio = i%2 == 0
		channels[i] = ch
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ch.NextFile(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, ch := range channels {
		name := ch.currentFilename()
		if name == "" || seen[name] {
			t.Errorf("duplicate or empty recording base: %q", name)
		}
		seen[name] = true
		if err := ch.Cleanup(); err != nil {
			t.Error(err)
		}
	}
}

func TestCreateNewFilePreservesExistingAudioOnFailure(t *testing.T) {
	base := filepath.Join(t.TempDir(), "alice")
	writeSidecar(t, base+audioSidecarSuffix, []byte("existing audio"))
	ch := bufferedTestChannel(&entity.ChannelConfig{Username: "alice", Pattern: base})
	ch.HasSeparateAudio = true
	ch.InitSegment = []byte("video init")
	ch.AudioInitSegment = []byte("audio init")
	if err := ch.CreateNewFile(base); !errors.Is(err, os.ErrExist) {
		t.Fatalf("error = %v, want exclusive-create failure", err)
	}
	data, err := os.ReadFile(base + audioSidecarSuffix)
	if err != nil || string(data) != "existing audio" {
		t.Fatalf("existing audio was modified: %q %v", data, err)
	}
	if _, err := os.Stat(base + videoSidecarSuffix); !os.IsNotExist(err) {
		t.Fatalf("new video should be rolled back: %v", err)
	}
	if ch.File != nil || ch.AudioFile != nil {
		t.Fatal("failed allocation must not leave open recording files")
	}
}

func TestRemuxKeepsNewRecordingWithReusedPattern(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	base := filepath.Join(dir, "alice")
	writeStaleSidecars(t, base,
		buildFragmentedMP4(t, "video", 90000, []byte{1, 2, 3}),
		buildFragmentedMP4(t, "audio", 44100, []byte{4, 5}))
	ch := bufferedTestChannel(&entity.ChannelConfig{Username: "alice", Pattern: base})
	ch.HasSeparateAudio = true
	ch.InitSegment = []byte("new video")
	ch.AudioInitSegment = []byte("new audio")
	if err := ch.NextFile(); err != nil {
		t.Fatal(err)
	}
	defer ch.File.Close()
	defer ch.AudioFile.Close()
	if merged, err := ch.RemuxOrphans(); err != nil || merged != 1 {
		t.Fatalf("merged=%d err=%v", merged, err)
	}
	for suffix, want := range map[string]string{videoSidecarSuffix: "new video", audioSidecarSuffix: "new audio"} {
		data, err := os.ReadFile(base + " (1)" + suffix)
		if err != nil || string(data) != want {
			t.Fatalf("new sidecar changed: %q %v", data, err)
		}
	}
	patterns, err := ch.wildcardPatterns()
	if err != nil || !ch.ownsRecording(base+" (1)", patterns) {
		t.Fatalf("collision-suffixed recordings must remain recoverable: %v", err)
	}
}

func TestRemuxCompressionCannotDeleteNextRecording(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	writeSidecar(t, filepath.Join(dir, "ffmpeg"), []byte(`#!/bin/sh
for arg in "$@"; do last="$arg"; done
case "$last" in
  *.mp4) exit 1 ;;
  *.mkv) printf 'compressed' > "$last" ;;
esac
`))
	if err := os.Chmod(filepath.Join(dir, "ffmpeg"), 0755); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(dir, "alice")
	writeStaleSidecars(t, base,
		buildFragmentedMP4(t, "video", 90000, []byte{1, 2, 3}),
		buildFragmentedMP4(t, "audio", 44100, []byte{4, 5}))
	ch := bufferedTestChannel(&entity.ChannelConfig{Username: "alice", Pattern: base, Compress: true})

	// Hold the encoder so recovery returns with an old source still queued.
	compressSlot <- struct{}{}
	held := true
	defer func() {
		if held {
			<-compressSlot
		}
	}()
	if merged, err := ch.RemuxOrphans(); err != nil || merged != 1 {
		t.Fatalf("merged=%d err=%v", merged, err)
	}
	if err := ch.NextFile(); err != nil {
		t.Fatal(err)
	}
	defer ch.File.Close()
	if _, err := ch.File.Write([]byte("new recording")); err != nil {
		t.Fatal(err)
	}
	newPath := ch.File.Name()
	if newPath == base+".ts" {
		t.Fatal("new recording reused the pending compression base")
	}
	recordingFileMu.Lock()
	allocationLocked := true
	defer func() {
		if allocationLocked {
			recordingFileMu.Unlock()
		}
	}()
	<-compressSlot
	held = false
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(base + ".mkv"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("compression did not create its output")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := os.Stat(base + ".mp4"); err != nil {
		t.Fatalf("allocation check must still see the encode source: %v", err)
	}
	recordingFileMu.Unlock()
	allocationLocked = false
	for {
		if _, err := os.Stat(base + ".mp4"); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queued compression did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	// Wait until the job's logging/move is also complete before restoring PATH.
	compressSlot <- struct{}{}
	<-compressSlot
	data, err := os.ReadFile(newPath)
	if err != nil || string(data) != "new recording" {
		t.Fatalf("compression modified the new recording: %q %v", data, err)
	}
}

func TestStartupPreservesPauseAndStop(t *testing.T) {
	for _, state := range []string{"paused", "stopped"} {
		t.Run(state, func(t *testing.T) {
			ch := bufferedTestChannel(&entity.ChannelConfig{Username: "alice", IsPaused: state == "paused"})
			if state == "stopped" {
				ch.Stop()
			}
			ch.Resume(0)
			if ch.monitorCtx != nil {
				t.Fatal("startup must not start a paused or stopped channel")
			}
			if state == "stopped" && ch.ResumeIfPaused(0) {
				t.Fatal("a deleted channel must not be resumed")
			}
		})
	}
}

func TestStopCancelsStaggeredStartup(t *testing.T) {
	ch := bufferedTestChannel(&entity.ChannelConfig{Username: "alice"})
	done := make(chan struct{})
	go func() { ch.Resume(3600); close(done) }()
	select {
	case <-ch.UpdateCh:
	case <-time.After(time.Second):
		t.Fatal("startup did not prepare its context")
	}
	ch.Stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop must cancel the pending start delay")
	}
	if ch.monitorCtx.Err() == nil {
		t.Fatal("pending Monitor context was not cancelled")
	}
}
