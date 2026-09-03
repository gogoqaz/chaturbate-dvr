package channel

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	videoSidecarSuffix = ".video.mp4"
	audioSidecarSuffix = ".audio.mp4"
)

// A live recording appends to its sidecars every few seconds, so anything
// younger than this is still being written and must not be merged.
const remuxQuietPeriod = 2 * time.Minute

// Caps how deep a scan descends, so a pattern rooted at the working directory
// cannot walk an entire disk.
const remuxScanDepth = 6

// Starting points for the values substituted into the very template
// GenerateFilename renders, then replaced by `*` to build the matcher.
const (
	wildcardSentinel    = "xWiLdCaRdx"
	wildcardSentinelSeq = 987654321
)

// RemuxOrphans merges every `<name>.video.mp4` / `<name>.audio.mp4` pair this
// channel left behind, and reports how many were merged.
func (ch *Channel) RemuxOrphans() (int, error) {
	return ch.remuxOrphans(true)
}

// RemuxOrphansQuiet is RemuxOrphans without the "nothing to do" chatter, for
// the automatic scan that runs whenever a channel starts.
func (ch *Channel) RemuxOrphansQuiet() (int, error) {
	return ch.remuxOrphans(false)
}

func (ch *Channel) remuxOrphans(announceEmpty bool) (int, error) {
	if !ch.remuxing.CompareAndSwap(false, true) {
		if announceEmpty {
			ch.Info("remux: a scan is already running")
		}
		return 0, nil
	}
	defer ch.remuxing.Store(false)

	bases, err := ch.findOrphanPairs()
	if err != nil {
		return 0, err
	}
	if len(bases) == 0 {
		if announceEmpty {
			ch.Info("remux: no unmerged audio/video files found")
		}
		return 0, nil
	}
	ch.Info("remux: found %d unmerged recording(s)", len(bases))

	var merged int
	for _, base := range bases {
		ok, err := ch.remuxPair(base)
		if err != nil {
			ch.Error("remux: %s: %s", filepath.Base(base), err.Error())
			continue
		}
		if ok {
			merged++
		}
	}
	ch.Info("remux: merged %d of %d recording(s)", merged, len(bases))
	return merged, nil
}

// remuxPair reports false without an error when the pair was already handled
// or the merged file failed the sanity check (which FinalizeMux logged).
func (ch *Channel) remuxPair(base string) (bool, error) {
	videoPath, audioPath := base+videoSidecarSuffix, base+audioSidecarSuffix

	// Another scan may have merged the pair between the walk and here.
	videoInfo, err := os.Stat(videoPath)
	if err != nil {
		return false, nil
	}
	audioInfo, err := os.Stat(audioPath)
	if err != nil {
		return false, nil
	}

	// The pair may have been picked up by a recording that started since the walk.
	if ok, _ := orphanPairReady(base, ch.currentFilename(), time.Now().Add(-remuxQuietPeriod)); !ok {
		return false, nil
	}

	ch.Info("remux: merging %s + %s", filepath.Base(videoPath), filepath.Base(audioPath))
	if err := ch.FinalizeMux(videoPath, audioPath, base+".mp4", videoInfo, audioInfo); err != nil {
		if errors.Is(err, ErrMuxRejected) || errors.Is(err, ErrMuxBusy) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// findOrphanPairs returns the base filenames (the path without the sidecar
// suffix) of every unmerged pair below the channel's recording directory.
func (ch *Channel) findOrphanPairs() ([]string, error) {
	matchers, err := ch.wildcardPatterns()
	if err != nil {
		return nil, err
	}
	root := patternRoot(ch.Config.Pattern)
	rootDepth := strings.Count(filepath.ToSlash(root), "/")
	cutoff := time.Now().Add(-remuxQuietPeriod)

	var bases []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subdirectory must not abort the whole scan.
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(entry.Name(), ".") || strings.Count(filepath.ToSlash(path), "/")-rootDepth > remuxScanDepth) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, videoSidecarSuffix) {
			return nil
		}
		base := strings.TrimSuffix(path, videoSidecarSuffix)
		if !ch.ownsRecording(base, matchers) {
			return nil
		}
		if ok, reason := orphanPairReady(base, ch.currentFilename(), cutoff); !ok {
			if reason != "" {
				ch.Info("remux: skipping %s (%s)", filepath.Base(base), reason)
			}
			return nil
		}
		bases = append(bases, base)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	return bases, nil
}

// orphanPairReady also returns a reason to log when a pair is deliberately
// left alone, empty when the files are simply not a pair.
func orphanPairReady(base, currentFilename string, cutoff time.Time) (bool, string) {
	if currentFilename != "" && base == currentFilename {
		return false, "still recording"
	}
	audioInfo, err := os.Stat(base + audioSidecarSuffix)
	if err != nil {
		// A lone video sidecar is a single-track recording, not a failed merge.
		return false, ""
	}
	videoInfo, err := os.Stat(base + videoSidecarSuffix)
	if err != nil {
		return false, ""
	}
	if videoInfo.ModTime().After(cutoff) || audioInfo.ModTime().After(cutoff) {
		return false, "still being written"
	}
	if _, err := os.Stat(base + ".mkv"); err == nil {
		return false, "merged file already exists"
	}
	// A .mp4 left next to its sidecars is a merge that died before it could
	// delete them, so retry it unless the output actually looks complete.
	if _, err := os.Stat(base + ".mp4"); err == nil {
		if ok, _ := muxOutputLooksValid(base+".mp4", videoInfo, audioInfo); ok {
			return false, "merged file already exists"
		}
	}
	return true, ""
}

// ownsRecording keeps a channel from merging another model's recording, which
// would file it under the wrong name and per-model folder.
func (ch *Channel) ownsRecording(base string, matchers []string) bool {
	name := filepath.ToSlash(filepath.Clean(base))
	for _, matcher := range matchers {
		if wildcardMatch(matcher, name) {
			return true
		}
	}
	return false
}

// wildcardPatterns renders the pattern with the time-varying fields
// wildcarded, twice, because `{{if .Sequence}}` renders two shapes.
func (ch *Channel) wildcardPatterns() ([]string, error) {
	tpl, err := template.New("filename").Parse(ch.Config.Pattern)
	if err != nil {
		return nil, fmt.Errorf("filename pattern error: %w", err)
	}
	text, seq := uniqueSentinels(ch.Config.Pattern + ch.Config.Username)

	patterns := make([]string, 0, 2)
	for _, sequence := range []int{0, seq} {
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, &Pattern{
			Username: ch.Config.Username,
			Sequence: sequence,
			Year:     text,
			Month:    text,
			Day:      text,
			Hour:     text,
			Minute:   text,
			Second:   text,
		}); err != nil {
			return nil, fmt.Errorf("template execution error: %w", err)
		}
		rendered := filepath.ToSlash(filepath.Clean(buf.String()))
		rendered = strings.ReplaceAll(rendered, text, "*")
		rendered = strings.ReplaceAll(rendered, strconv.Itoa(seq), "*")
		patterns = append(patterns, rendered)
	}
	return patterns, nil
}

// uniqueSentinels picks values absent from the pattern's own literals, so the
// substitution cannot punch a wildcard into a username or a fixed segment.
func uniqueSentinels(literals string) (string, int) {
	text := wildcardSentinel
	for strings.Contains(literals, text) {
		text += "x"
	}
	seq := wildcardSentinelSeq
	for strings.Contains(literals, strconv.Itoa(seq)) {
		seq++
	}
	return text, seq
}

// PatternIsChannelSpecific reports whether the filename encodes the username,
// the only evidence a scan has that a leftover recording belongs to a channel.
func (ch *Channel) PatternIsChannelSpecific() bool {
	tpl, err := template.New("filename").Parse(ch.Config.Pattern)
	if err != nil {
		return false
	}
	render := func(username string) (string, error) {
		var buf bytes.Buffer
		err := tpl.Execute(&buf, &Pattern{Username: username})
		return buf.String(), err
	}
	a, errA := render("aaaaaaaa")
	b, errB := render("bbbbbbbb")
	return errA == nil && errB == nil && a != b
}

// patternRoot returns the deepest directory of the pattern holding no
// placeholder, so "videos/{{.Year}}/..." is scanned in full.
func patternRoot(pattern string) string {
	prefix := pattern
	if i := strings.Index(pattern, "{{"); i >= 0 {
		prefix = pattern[:i]
	}
	if i := strings.LastIndexAny(prefix, `/\`); i >= 0 {
		prefix = prefix[:i+1]
	} else {
		prefix = ""
	}
	return filepath.Clean(filepath.FromSlash(prefix))
}

// wildcardMatch matches segment by segment, so `*` can never swallow a
// directory boundary.
func wildcardMatch(pattern, name string) bool {
	patternSegments := strings.Split(pattern, "/")
	nameSegments := strings.Split(name, "/")
	if len(patternSegments) != len(nameSegments) {
		return false
	}
	for i := range patternSegments {
		if !matchSegment(patternSegments[i], nameSegments[i]) {
			return false
		}
	}
	return true
}

// matchSegment matches one segment against a pattern whose only special
// character is `*`, backtracking through the most recent star.
func matchSegment(pattern, name string) bool {
	var (
		p, n         int
		starP, starN = -1, 0
	)
	for n < len(name) {
		switch {
		case p < len(pattern) && pattern[p] == name[n]:
			p++
			n++
		case p < len(pattern) && pattern[p] == '*':
			starP, starN = p, n
			p++
		case starP >= 0:
			p = starP + 1
			starN++
			n = starN
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
