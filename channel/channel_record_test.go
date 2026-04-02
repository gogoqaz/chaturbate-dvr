package channel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/teacat/chaturbate-dvr/entity"
)

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
