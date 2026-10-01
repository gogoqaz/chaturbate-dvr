package channel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalPathResolvesMissingDescendants(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	path := filepath.Join(alias, "new", "nested", "alice.mp4")
	want := filepath.Join(realDir, "new", "nested", "alice.mp4")
	if got, err := canonicalPath(path); err != nil || got != want {
		t.Fatalf("canonicalPath(%q) = %q, %v; want %q", path, got, err, want)
	}
	if !remuxRootsOverlap(filepath.Join(alias, "new"), filepath.Join(realDir, "new")) {
		t.Fatal("missing roots under a directory alias must still overlap")
	}
}

func TestCanonicalPathRejectsBrokenAliases(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"dangling", "loop"} {
		target := filepath.Join(dir, "missing")
		if name == "loop" {
			target = filepath.Join(dir, name)
		}
		alias := filepath.Join(dir, name)
		if err := os.Symlink(target, alias); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		path := filepath.Join(alias, "new", "alice.mp4")
		if _, err := canonicalPath(path); err == nil {
			t.Fatalf("%s alias must not become a lexical mux claim", name)
		}
		ch := bufferedTestChannel(nil)
		if err := ch.FinalizeMux("", "", path, nil, nil); err == nil {
			t.Fatalf("%s alias must reject muxing before opening an output", name)
		}
	}
}
