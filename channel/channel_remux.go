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
	"text/template/parse"
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

// RemuxOrphans merges every `<name>.video.mp4` / `<name>.audio.mp4` pair this
// channel left behind, and reports how many were merged.
func (ch *Channel) RemuxOrphans(peers ...*Channel) (int, error) {
	merged, _, err := ch.remuxOrphans(true, peers)
	return merged, err
}

// RemuxOrphansQuiet runs a startup scan and returns the delay until fresh
// leftovers can be retried, or zero when no delayed scan is needed.
func (ch *Channel) RemuxOrphansQuiet(peers ...*Channel) (time.Duration, error) {
	_, retryAt, err := ch.remuxOrphans(false, peers)
	if retryAt.IsZero() {
		return 0, err
	}
	return max(time.Until(retryAt), time.Nanosecond), err
}

func (ch *Channel) remuxOrphans(announceEmpty bool, peers []*Channel) (int, time.Time, error) {
	if !ch.remuxing.CompareAndSwap(false, true) {
		if announceEmpty {
			ch.Info("remux: a scan is already running")
		}
		// Startup recovery must not lose its retry when a manual scan owns
		// the channel. The next scan will recheck the files' quiet period.
		return 0, time.Now().Add(remuxQuietPeriod), nil
	}
	defer ch.remuxing.Store(false)

	bases, retryAt, err := ch.findOrphanPairs(peers...)
	if err != nil {
		return 0, time.Time{}, err
	}
	if len(bases) == 0 {
		if announceEmpty {
			ch.Info("remux: no unmerged audio/video files found")
		}
		return 0, retryAt, nil
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
	return merged, retryAt, nil
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
	if ok, _, _ := orphanPairReady(base, ch.currentFilename(), time.Now().Add(-remuxQuietPeriod)); !ok {
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
func (ch *Channel) findOrphanPairs(peers ...*Channel) ([]string, time.Time, error) {
	matchers, err := ch.wildcardPatterns()
	if err != nil {
		return nil, time.Time{}, err
	}
	root := patternRoot(ch.Config.Pattern)
	var otherMatchers [][]string
	for _, peer := range peers {
		if peer == ch {
			continue
		}
		// A peer in a separate directory cannot own these recordings. Keep
		// peers with parent traversal conservative: templates can escape the
		// literal directory prefix used by patternRoot.
		mayTraverse := false
		if i := strings.Index(peer.Config.Pattern, "{{"); i >= 0 {
			mayTraverse = strings.Contains(peer.Config.Pattern[i:], "..")
		}
		if !mayTraverse && !remuxRootsOverlap(root, patternRoot(peer.Config.Pattern)) {
			continue
		}
		patterns, err := peer.wildcardPatterns()
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("cannot establish ownership for %s: %w", peer.Config.Username, err)
		}
		otherMatchers = append(otherMatchers, patterns)
	}
	rootDepth := strings.Count(filepath.ToSlash(root), "/")
	cutoff := time.Now().Add(-remuxQuietPeriod)

	var bases []string
	var retryAt time.Time
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
		// Compare the actual filename, rather than guessing whether two
		// wildcard patterns intersect. Only a unique owner may repair it.
		for _, patterns := range otherMatchers {
			if ch.ownsRecording(base, patterns) {
				ch.Info("remux: skipping %s (more than one channel matches this filename)", filepath.Base(base))
				return nil
			}
		}
		if ok, reason, quietAt := orphanPairReady(base, ch.currentFilename(), cutoff); !ok {
			if !quietAt.IsZero() && (retryAt.IsZero() || quietAt.Before(retryAt)) {
				retryAt = quietAt
			}
			if reason != "" {
				ch.Info("remux: skipping %s (%s)", filepath.Base(base), reason)
			}
			return nil
		}
		bases = append(bases, base)
		return nil
	})
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("scan %s: %w", root, err)
	}
	return bases, retryAt, nil
}

// Directory identity uses absolute paths and resolves existing symlinks.
// Unknown roots stay conservative; a failed lookup must not prove isolation.
func remuxRootsOverlap(a, b string) bool {
	roots := []string{a, b}
	for i, root := range roots {
		absolute, err := filepath.Abs(root)
		if err != nil {
			return true
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err == nil {
			absolute = resolved
		} else if !os.IsNotExist(err) {
			return true
		}
		roots[i] = absolute
	}
	for i := range roots {
		relative, err := filepath.Rel(roots[i], roots[1-i])
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// orphanPairReady also returns a reason to log when a pair is deliberately
// left alone, empty when the files are simply not a pair.
func orphanPairReady(base, currentFilename string, cutoff time.Time) (bool, string, time.Time) {
	if currentFilename != "" {
		current, currentErr := filepath.Abs(currentFilename)
		candidate, candidateErr := filepath.Abs(base)
		if currentErr != nil || candidateErr != nil || current == candidate {
			return false, "still recording", time.Time{}
		}
	}
	audioInfo, err := os.Stat(base + audioSidecarSuffix)
	if err != nil {
		// A lone video sidecar is a single-track recording, not a failed merge.
		return false, "", time.Time{}
	}
	videoInfo, err := os.Stat(base + videoSidecarSuffix)
	if err != nil {
		return false, "", time.Time{}
	}
	if videoInfo.ModTime().After(cutoff) || audioInfo.ModTime().After(cutoff) {
		quietAt := videoInfo.ModTime()
		if audioInfo.ModTime().After(quietAt) {
			quietAt = audioInfo.ModTime()
		}
		return false, "still being written", quietAt.Add(remuxQuietPeriod)
	}
	if _, err := os.Stat(base + ".mkv"); err == nil {
		return false, "merged file already exists", time.Time{}
	}
	// A .mp4 left next to its sidecars is a merge that died before it could
	// delete them, so retry it unless the output actually looks complete.
	if _, err := os.Stat(base + ".mp4"); err == nil {
		if ok, _ := muxOutputLooksValid(base+".mp4", videoInfo, audioInfo); ok && muxHasBothTracks(base+".mp4") {
			return false, "merged file already exists", time.Time{}
		}
	}
	return true, "", time.Time{}
}

// ownsRecording keeps a channel from merging another model's recording, which
// would file it under the wrong name and per-model folder.
func (ch *Channel) ownsRecording(base string, matchers []string) bool {
	absolute, err := filepath.Abs(base)
	if err != nil {
		return false
	}
	name := filepath.ToSlash(absolute)
	for _, matcher := range matchers {
		if wildcardMatch(matcher, name) {
			return true
		}
		// NextFile adds a numeric suffix when the requested base is in use.
		if i := strings.LastIndex(name, " ("); i >= 0 && strings.HasSuffix(name, ")") {
			if n, err := strconv.Atoi(name[i+2 : len(name)-1]); err == nil && n > 0 && wildcardMatch(matcher, name[:i]) {
				return true
			}
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
	patterns := make([]string, 0, 2)
	for _, sequence := range []bool{false, true} {
		rendered, err := ch.wildcardList(tpl.Tree.Root, sequence)
		if err != nil {
			return nil, err
		}
		absolute, err := filepath.Abs(rendered)
		if err != nil {
			return nil, fmt.Errorf("resolve filename pattern: %w", err)
		}
		patterns = append(patterns, filepath.ToSlash(absolute))
	}
	return patterns, nil
}

// Render wildcard fields structurally. Literal text and username actions can
// never become wildcards, and unsupported date transformations fail closed.
func (ch *Channel) wildcardList(list *parse.ListNode, sequence bool) (string, error) {
	var out strings.Builder
	if list == nil {
		return "", nil
	}
	for _, node := range list.Nodes {
		switch node := node.(type) {
		case *parse.TextNode:
			if strings.Contains(string(node.Text), "*") {
				return "", fmt.Errorf("remux does not support literal * in filename patterns")
			}
			out.Write(node.Text)
		case *parse.ActionNode:
			if len(node.Pipe.Decl) != 0 {
				return "", fmt.Errorf("remux does not support filename template variables")
			}
			field := simplePatternField(node.Pipe)
			switch field {
			case "Username":
				out.WriteString(ch.Config.Username)
			case "Year", "Month", "Day", "Hour", "Minute", "Second":
				out.WriteString("*")
			case "Sequence":
				if sequence {
					out.WriteString("*")
				} else {
					out.WriteString("0")
				}
			default:
				// Static formatting of the username is safe to render; time
				// fields and template variables cannot be inferred this way.
				for _, cmd := range node.Pipe.Cmds {
					for _, arg := range cmd.Args {
						switch arg := arg.(type) {
						case *parse.FieldNode:
							if len(arg.Ident) != 1 || arg.Ident[0] != "Username" {
								return "", fmt.Errorf("remux cannot infer filename action %s", node)
							}
						case *parse.StringNode, *parse.NumberNode, *parse.IdentifierNode, *parse.BoolNode:
						default:
							return "", fmt.Errorf("remux cannot infer filename action %s", node)
						}
					}
				}
				tpl, err := template.New("action").Parse(node.String())
				if err != nil {
					return "", err
				}
				var value bytes.Buffer
				if err := tpl.Execute(&value, &Pattern{Username: ch.Config.Username}); err != nil {
					return "", err
				}
				if strings.Contains(value.String(), "*") {
					return "", fmt.Errorf("remux does not support literal * in filename actions")
				}
				out.WriteString(value.String())
			}
		case *parse.IfNode:
			if simplePatternField(node.Pipe) != "Sequence" {
				return "", fmt.Errorf("remux only supports filename conditions on .Sequence")
			}
			branch := node.ElseList
			if sequence {
				branch = node.List
			}
			value, err := ch.wildcardList(branch, sequence)
			if err != nil {
				return "", err
			}
			out.WriteString(value)
		default:
			return "", fmt.Errorf("remux cannot infer filename template %s", node)
		}
	}
	return out.String(), nil
}

func simplePatternField(pipe *parse.PipeNode) string {
	if len(pipe.Decl) == 0 && len(pipe.Cmds) == 1 && len(pipe.Cmds[0].Args) == 1 {
		if field, ok := pipe.Cmds[0].Args[0].(*parse.FieldNode); ok && len(field.Ident) == 1 {
			return field.Ident[0]
		}
	}
	return ""
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
