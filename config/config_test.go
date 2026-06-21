package config

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/urfave/cli/v2"
)

func TestNewDoesNotAutoEnableCompressWhenFFmpegExists(t *testing.T) {
	set := newConfigFlagSet(t)
	t.Setenv("PATH", fakeFFmpegDir(t))

	conf, err := New(newConfigContext(set))
	if err != nil {
		t.Fatalf("new config: %v", err)
	}

	if conf.Compress {
		t.Fatal("compress = true, want false unless --compress is explicitly set")
	}
}

func TestNewEnablesCompressWhenFlagIsExplicit(t *testing.T) {
	set := newConfigFlagSet(t, "--compress")
	t.Setenv("PATH", fakeFFmpegDir(t))

	conf, err := New(newConfigContext(set))
	if err != nil {
		t.Fatalf("new config: %v", err)
	}

	if !conf.Compress {
		t.Fatal("compress = false, want true when --compress is explicitly set")
	}
}

func newConfigFlagSet(t *testing.T, args ...string) *flag.FlagSet {
	t.Helper()

	set := flag.NewFlagSet("test", flag.ContinueOnError)
	compressFlag := &cli.BoolFlag{Name: "compress"}
	if err := compressFlag.Apply(set); err != nil {
		t.Fatalf("apply compress flag: %v", err)
	}
	if err := set.Parse(args); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	return set
}

func newConfigContext(set *flag.FlagSet) *cli.Context {
	return cli.NewContext(&cli.App{Version: "test"}, set, nil)
}

func fakeFFmpegDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return dir
}
