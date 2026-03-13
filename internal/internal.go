package internal

import (
	"fmt"
	"regexp"
	"strconv"
)

// FormatDuration converts a float64 duration (in seconds) to h:m:s format.
func FormatDuration(duration float64) string {
	if duration == 0 {
		return ""
	}
	var (
		hours   = int(duration) / 3600
		minutes = (int(duration) % 3600) / 60
		seconds = int(duration) % 60
	)
	return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
}

// FormatFilesize converts an int filesize in bytes to a human-readable string (KB, MB, GB).
func FormatFilesize(filesize int) string {
	if filesize == 0 {
		return ""
	}
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case filesize >= GB:
		return fmt.Sprintf("%.2f GB", float64(filesize)/float64(GB))
	case filesize >= MB:
		return fmt.Sprintf("%.2f MB", float64(filesize)/float64(MB))
	case filesize >= KB:
		return fmt.Sprintf("%.2f KB", float64(filesize)/float64(KB))
	default:
		return fmt.Sprintf("%d bytes", filesize)
	}
}

var (
	// Old format: media_w1920_12345.ts
	segmentSeqTSRegexp = regexp.MustCompile(`_(\d+)\.ts$`)
	// New LL-HLS format: seg_4_6543_video_11903984827994253865_llhls.m4s?session=xxx
	segmentSeqM4SRegexp = regexp.MustCompile(`seg_\d+_(\d+)_`)
)

// SegmentSeq extracts the segment sequence number from a filename.
func SegmentSeq(filename string) int {
	// Old format: xxx_12345.ts
	if match := segmentSeqTSRegexp.FindStringSubmatch(filename); len(match) > 1 {
		if number, err := strconv.Atoi(match[1]); err == nil {
			return number
		}
	}
	// New LL-HLS format: seg_X_12345_video_xxx.m4s
	if match := segmentSeqM4SRegexp.FindStringSubmatch(filename); len(match) > 1 {
		if number, err := strconv.Atoi(match[1]); err == nil {
			return number
		}
	}
	return -1
}
