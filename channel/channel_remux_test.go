package channel

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/teacat/chaturbate-dvr/entity"
)

// No Publisher is needed for file/lifecycle tests. Buffered notifications keep
// these tests independent of the UI's unrelated shared-state races.
func bufferedTestChannel(conf *entity.ChannelConfig) *Channel {
	return &Channel{
		Config: conf, LogCh: make(chan string, 100), UpdateCh: make(chan bool, 100),
		CancelFunc: func() {}, PauseCancelFunc: func() {},
	}
}

func combinedMP4(t *testing.T) []byte {
	t.Helper()
	video, err := mp4.DecodeFile(bytes.NewReader(buildFragmentedMP4(t, "video", 90000, []byte{1, 2, 3})))
	if err != nil {
		t.Fatal(err)
	}
	audio, err := mp4.DecodeFile(bytes.NewReader(buildFragmentedMP4(t, "audio", 44100, []byte{4, 5})))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := writeCombinedFragmentedMP4(&out, video, audio, func(string) {}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

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

	bases, _, err := ch.findOrphanPairs()
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

	bases, _, err := ch.findOrphanPairs()
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
	ch.setCurrentFilename(current)

	// Already merged in an earlier run.
	merged := filepath.Join(dir, "alice_2025-09-03_12-00-00")
	writeStaleSidecars(t, merged, []byte("video"), []byte("audio"))
	writeSidecar(t, merged+".mp4", combinedMP4(t))

	// Single-track recording: no audio to merge with.
	lone := filepath.Join(dir, "alice_2025-09-03_13-00-00")
	writeSidecar(t, lone+videoSidecarSuffix, []byte("video"))

	bases, _, err := ch.findOrphanPairs()
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
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	claim := filepath.Join(resolvedDir, filepath.Base(output))
	inFlightMux.Store(claim, struct{}{})
	defer inFlightMux.Delete(claim)

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

func TestRemuxOrphansRetriesAnIncompleteOutput(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir) // no ffmpeg: exercises the native muxer

	base := filepath.Join(dir, "alice_2025-09-03_10-00-00")
	writeStaleSidecars(t, base,
		buildFragmentedMP4(t, "video", 90000, []byte{0x00, 0x00, 0x00, 0x01, 0x67}),
		buildFragmentedMP4(t, "audio", 44100, []byte{0xFF, 0xF1}))
	// A merge that died before it could delete the sidecars.
	writeSidecar(t, base+".mp4", []byte("truncated"))

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
	if _, err := mp4.ReadMP4File(base + ".mp4"); err != nil {
		t.Fatalf("truncated output must be replaced, ReadMP4File() error = %v", err)
	}
}

func TestFinalizeMuxRemovesAPartialOutputWhenBothMuxersFail(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir) // no ffmpeg, and the payload is not a valid fMP4

	base := filepath.Join(dir, "alice_2025-09-03_10-00-00")
	writeStaleSidecars(t, base, []byte("not-an-mp4"), []byte("not-an-mp4"))
	output := base + ".mp4"
	// Stand in for the partial file a crashed ffmpeg leaves behind.
	writeSidecar(t, output, []byte("partial"))

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

	if err := ch.FinalizeMux(base+videoSidecarSuffix, base+audioSidecarSuffix, output, videoInfo, audioInfo); err == nil {
		t.Fatal("FinalizeMux() error = nil, want a mux failure")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("partial output must be removed, stat err = %v", err)
	}
}

func TestWildcardPatternsKeepsSentinelLookalikesLiteral(t *testing.T) {
	t.Parallel()

	// The username carries the sequence sentinel, the pattern the text one.
	ch := New(&entity.ChannelConfig{
		Username: "alice987654321",
		Pattern:  "xWiLdCaRdx/{{.Username}}_{{.Year}}",
	})

	patterns, err := ch.wildcardPatterns()
	if err != nil {
		t.Fatalf("wildcardPatterns() error = %v", err)
	}
	for _, pattern := range patterns {
		if !strings.Contains(pattern, "/xWiLdCaRdx/alice987654321_") {
			t.Fatalf("pattern = %q, want the literals preserved", pattern)
		}
	}
	if ch.ownsRecording("xWiLdCaRdx/alice9876543219/2025", patterns) {
		t.Fatal("a lookalike username must not be claimed")
	}
	if !ch.ownsRecording("xWiLdCaRdx/alice987654321_2025", patterns) {
		t.Fatal("the channel's own recording must still be claimed")
	}
}

func TestFindOrphanPairsRequiresOneOwnerForEachFilename(t *testing.T) {
	dir := t.TempDir()
	alice := bufferedTestChannel(&entity.ChannelConfig{Username: "alice", Pattern: filepath.Join(dir, "{{.Username}}_{{.Year}}_10")})
	other := bufferedTestChannel(&entity.ChannelConfig{Username: "2026", Pattern: filepath.Join(dir, "alice_{{.Username}}_{{.Month}}")})
	shared := filepath.Join(dir, "alice_2026_10")
	mine := filepath.Join(dir, "alice_2025_10")
	writeStaleSidecars(t, shared, []byte("video"), []byte("audio"))
	writeStaleSidecars(t, mine, []byte("video"), []byte("audio"))
	peers := []*Channel{alice, other}
	for _, ch := range peers {
		bases, _, err := ch.findOrphanPairs(peers...)
		if err != nil {
			t.Fatal(err)
		}
		for _, base := range bases {
			if base == shared {
				t.Fatal("a filename matched by two channels must not be claimed")
			}
		}
		if ch == alice && (len(bases) != 1 || bases[0] != mine) {
			t.Fatalf("unique recording must still be recoverable: %v", bases)
		}
	}
}

func TestStartupRemuxRetriesFreshLeftoversButNotActiveFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	base := filepath.Join(dir, "alice")
	active := base + " (1)"
	video := buildFragmentedMP4(t, "video", 90000, []byte{1, 2, 3})
	audio := buildFragmentedMP4(t, "audio", 44100, []byte{4, 5})
	for _, name := range []string{base, active} {
		writeSidecar(t, name+videoSidecarSuffix, video)
		writeSidecar(t, name+audioSidecarSuffix, audio)
	}
	ch := bufferedTestChannel(&entity.ChannelConfig{Username: "alice", Pattern: base})
	ch.setCurrentFilename(active)
	delay, err := ch.RemuxOrphansQuiet(ch)
	if err != nil || delay <= 0 || delay > remuxQuietPeriod {
		t.Fatalf("fresh leftovers need a quiet-period retry: delay=%v err=%v", delay, err)
	}
	if _, err := os.Stat(base + ".mp4"); !os.IsNotExist(err) {
		t.Fatalf("fresh sidecars must not be merged yet: %v", err)
	}
	writeStaleSidecars(t, base, video, audio)
	delay, err = ch.RemuxOrphansQuiet(ch)
	if err != nil || delay != 0 || !muxHasBothTracks(base+".mp4") {
		t.Fatalf("quiet leftovers should recover without another retry: delay=%v err=%v", delay, err)
	}
	for _, suffix := range []string{videoSidecarSuffix, audioSidecarSuffix} {
		if _, err := os.Stat(active + suffix); err != nil {
			t.Fatalf("active file must stay untouched and not schedule retries: %v", err)
		}
	}
}

func TestFindOrphanPairsIsolatesUnsupportedPeerRoots(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "alice")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(root, "alice")
	writeStaleSidecars(t, base, []byte("video"), []byte("audio"))
	ch := bufferedTestChannel(&entity.ChannelConfig{Username: "alice", Pattern: filepath.Join(root, "{{.Username}}")})
	condition := `{{if eq .Year "2026"}}new{{else}}old{{end}}`
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, filepath.Join(dir, "alice-other"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		pattern string
		blocked bool
	}{
		{"sibling with same prefix", filepath.Join(dir, "alice-other", condition), false},
		{"relative sibling", filepath.Join(relative, condition), false},
		{"overlapping root", filepath.Join(root, condition), true},
		{"parent root", filepath.Join(dir, condition), true},
		{"parent traversal", filepath.Join(dir, "alice-other") + "/" + condition + "/../..", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			peer := bufferedTestChannel(&entity.ChannelConfig{Username: "bob", Pattern: tt.pattern})
			bases, _, err := ch.findOrphanPairs(ch, peer)
			if tt.blocked {
				if err == nil {
					t.Fatal("uncertain ownership in an overlapping root must remain blocked")
				}
			} else if err != nil || len(bases) != 1 || bases[0] != base {
				t.Fatalf("unrelated peer must not disable recovery: bases=%v err=%v", bases, err)
			}
			if _, _, err := peer.findOrphanPairs(ch, peer); err == nil {
				t.Fatal("unsupported peer's own scan must remain blocked")
			}
		})
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	peer := bufferedTestChannel(&entity.ChannelConfig{Username: "bob", Pattern: filepath.Join(alias, condition)})
	if _, _, err := ch.findOrphanPairs(ch, peer); err == nil {
		t.Fatal("symlink alias must not be treated as a separate root")
	}
}

func TestRemuxRecognizesRelativeAndAbsoluteAliases(t *testing.T) {
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, dir)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(dir, "alice")
	writeStaleSidecars(t, base, []byte("video"), []byte("audio"))
	a := bufferedTestChannel(&entity.ChannelConfig{Username: "alice", Pattern: filepath.Join(relative, "{{.Username}}")})
	b := bufferedTestChannel(&entity.ChannelConfig{Username: "bob", Pattern: base})
	for _, ch := range []*Channel{a, b} {
		bases, _, err := ch.findOrphanPairs(a, b)
		if err != nil || len(bases) != 0 {
			t.Fatalf("aliases must be ambiguous: bases=%v err=%v", bases, err)
		}
	}
	if ready, _, _ := orphanPairReady(base, filepath.Join(relative, "alice"), time.Now()); ready {
		t.Fatal("relative current filename must protect the absolute candidate")
	}
	output := base + ".mp4"
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	claim := filepath.Join(resolvedDir, filepath.Base(output))
	inFlightMux.Store(claim, struct{}{})
	defer inFlightMux.Delete(claim)
	if err := a.FinalizeMux("", "", filepath.Join(relative, "alice.mp4"), nil, nil); !errors.Is(err, ErrMuxBusy) {
		t.Fatalf("relative output must share the absolute claim: %v", err)
	}
}

func TestRemuxRecognizesSymlinkAliases(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "recordings")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	base := filepath.Join(root, "alice_2026")
	writeStaleSidecars(t, base, []byte("video"), []byte("audio"))
	a := bufferedTestChannel(&entity.ChannelConfig{Username: "alice", Pattern: filepath.Join(alias, "{{.Username}}_{{.Year}}")})
	b := bufferedTestChannel(&entity.ChannelConfig{Username: "bob", Pattern: filepath.Join(root, "alice_{{.Year}}")})
	for _, ch := range []*Channel{a, b} {
		bases, _, err := ch.findOrphanPairs(ch)
		if err != nil || len(bases) != 1 {
			t.Fatalf("scan must follow its root alias: bases=%v err=%v", bases, err)
		}
		bases, _, err = ch.findOrphanPairs(a, b)
		if err != nil || len(bases) != 0 {
			t.Fatalf("both aliases must recognize ambiguous ownership: bases=%v err=%v", bases, err)
		}
	}
	aliasBase := filepath.Join(alias, "alice_2026")
	for _, paths := range [][2]string{{base, aliasBase}, {aliasBase, base}} {
		if ready, _, _ := orphanPairReady(paths[0], paths[1], time.Now()); ready {
			t.Fatal("a current recording must be protected through either alias")
		}
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	// The output does not exist yet: resolving only complete files would
	// give the two simultaneous muxes different claims.
	output := filepath.Join(resolvedRoot, "alice_2026.mp4")
	inFlightMux.Store(output, struct{}{})
	defer inFlightMux.Delete(output)
	for _, root := range []string{root, alias} {
		if err := a.FinalizeMux("", "", filepath.Join(root, "alice_2026.mp4"), nil, nil); !errors.Is(err, ErrMuxBusy) {
			t.Fatalf("aliased output must share one mux claim: %v", err)
		}
	}
}

func TestRemuxOwnershipResolvesSymlinksInWildcardDirectories(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "recordings")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	aliasRoot := filepath.Join(dir, "dated")
	if err := os.MkdirAll(aliasRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(aliasRoot, "2026")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	base := filepath.Join(root, "alice")
	writeStaleSidecars(t, base, []byte("video"), []byte("audio"))
	a := bufferedTestChannel(&entity.ChannelConfig{Username: "alice", Pattern: filepath.Join(root, "{{.Username}}")})
	b := bufferedTestChannel(&entity.ChannelConfig{Username: "bob", Pattern: filepath.Join(aliasRoot, "{{.Year}}", "alice")})
	if bases, _, err := a.findOrphanPairs(a, b); err != nil || len(bases) != 0 {
		t.Fatalf("a wildcard directory alias must still establish peer ownership: bases=%v err=%v", bases, err)
	}
}

func TestMuxKeepsSidecarsUntilAllocationCheckFinishes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	base := filepath.Join(dir, "alice")
	writeStaleSidecars(t, base,
		buildFragmentedMP4(t, "video", 90000, []byte{1, 2, 3}),
		buildFragmentedMP4(t, "audio", 44100, []byte{4, 5}))
	video, _ := os.Stat(base + videoSidecarSuffix)
	audio, _ := os.Stat(base + audioSidecarSuffix)
	ch := bufferedTestChannel(&entity.ChannelConfig{Username: "alice", Pattern: base})
	recordingFileMu.Lock()
	locked := true
	defer func() {
		if locked {
			recordingFileMu.Unlock()
		}
	}()
	done := make(chan error, 1)
	go func() {
		done <- ch.FinalizeMux(base+videoSidecarSuffix, base+audioSidecarSuffix, base+".mp4", video, audio)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !muxHasBothTracks(base + ".mp4") {
		if time.Now().After(deadline) {
			t.Fatal("mux did not create its output")
		}
		time.Sleep(time.Millisecond)
	}
	for _, suffix := range []string{videoSidecarSuffix, audioSidecarSuffix} {
		if _, err := os.Stat(base + suffix); err != nil {
			t.Fatalf("allocation check must still see the original sidecars: %v", err)
		}
	}
	recordingFileMu.Unlock()
	locked = false
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWildcardPatternsRejectsUnsupportedTimeTransforms(t *testing.T) {
	for _, pattern := range []string{
		`videos/{{.Username}}_{{printf "%.2s" .Year}}`,
		`videos/{{if eq .Year "2026"}}new{{else}}old{{end}}`,
		`videos/{{$name := .Username}}{{$name}}_{{.Year}}`,
		`videos/literal*_{{.Username}}`,
	} {
		ch := bufferedTestChannel(&entity.ChannelConfig{Username: "alice", Pattern: pattern})
		if _, err := ch.wildcardPatterns(); err == nil {
			t.Errorf("unsupported pattern must fail explicitly: %s", pattern)
		}
	}
	ch := bufferedTestChannel(&entity.ChannelConfig{Username: "alice", Pattern: `videos/{{printf "%.1s" .Username}}_{{.Year}}`})
	patterns, err := ch.wildcardPatterns()
	if err != nil || !ch.ownsRecording("videos/a_2026", patterns) {
		t.Fatalf("static username formatting should remain supported: %v, %v", patterns, err)
	}
}

func TestRemuxRetriesLargeOutputWithoutMoov(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	base := filepath.Join(dir, "alice")
	writeStaleSidecars(t, base,
		buildFragmentedMP4(t, "video", 90000, []byte{1, 2, 3}),
		buildFragmentedMP4(t, "audio", 44100, []byte{4, 5}))
	// A valid, large mdat box without a moov: size alone must not skip repair.
	var partial bytes.Buffer
	mdat := &mp4.MdatBox{}
	mdat.AddSampleData(make([]byte, 4096))
	if err := mdat.Encode(&partial); err != nil {
		t.Fatal(err)
	}
	writeSidecar(t, base+".mp4", partial.Bytes())
	ch := bufferedTestChannel(&entity.ChannelConfig{Username: "alice", Pattern: base})
	if merged, err := ch.RemuxOrphans(); err != nil || merged != 1 {
		t.Fatalf("large incomplete output must be retried: merged=%d err=%v", merged, err)
	}
	if !muxHasBothTracks(base + ".mp4") {
		t.Fatal("repaired output must contain readable video and audio tracks")
	}
}

func TestMuxHasBothTracksRejectsTruncatedOrSingleTrackOutput(t *testing.T) {
	dir := t.TempDir()
	valid := combinedMP4(t)
	for _, tt := range []struct {
		name string
		data []byte
		want bool
	}{
		{"complete", valid, true},
		{"truncated payload", valid[:len(valid)-1], false},
		{"single track", buildFragmentedMP4(t, "video", 90000, []byte{1}), false},
	} {
		path := filepath.Join(dir, tt.name+".mp4")
		writeSidecar(t, path, tt.data)
		if got := muxHasBothTracks(path); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
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
