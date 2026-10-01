package channel

import (
	"os"
	"path/filepath"
	"strings"
)

// canonicalPath resolves directory aliases even when the recording base or
// mux output does not exist yet. Broken links and lookup errors fail closed.
func canonicalPath(path string) (string, error) {
	prefix, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var tail string
	for {
		resolved, err := filepath.EvalSymlinks(prefix)
		if err == nil {
			return filepath.Join(resolved, tail), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		// An existing entry that cannot be resolved is a dangling link,
		// not a missing filename that can be appended to its parent.
		if _, lookupErr := os.Lstat(prefix); !os.IsNotExist(lookupErr) {
			return "", err
		}
		parent := filepath.Dir(prefix)
		if parent == prefix {
			return "", err
		}
		tail = filepath.Join(filepath.Base(prefix), tail)
		prefix = parent
	}
}

// Resolve existing wildcard directories before comparing ownership. Resolving
// only the literal prefix would miss aliases selected by a date field.
// The filename wildcard stays intact; only * has special meaning here.
func canonicalWildcardPatterns(absolute string) ([]string, error) {
	directory, filename := filepath.Dir(absolute), filepath.Base(absolute)
	root := filepath.VolumeName(directory) + string(filepath.Separator)
	directories := []string{root}
	for _, segment := range strings.Split(strings.TrimPrefix(directory, root), string(filepath.Separator)) {
		if segment == "" {
			continue
		}
		var next []string
		for _, parent := range directories {
			if !strings.Contains(segment, "*") {
				next = append(next, filepath.Join(parent, segment))
				continue
			}
			entries, err := os.ReadDir(parent)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				if !matchSegment(segment, entry.Name()) {
					continue
				}
				path := filepath.Join(parent, entry.Name())
				info, err := os.Stat(path)
				if err != nil {
					return nil, err
				}
				if info.IsDir() {
					next = append(next, path)
				}
			}
		}
		directories = next
	}
	patterns := make([]string, 0, len(directories))
	for _, directory := range directories {
		resolved, err := canonicalPath(directory)
		if err != nil {
			return nil, err
		}
		patterns = append(patterns, filepath.ToSlash(filepath.Join(resolved, filename)))
	}
	return patterns, nil
}
