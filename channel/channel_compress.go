package channel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/teacat/chaturbate-dvr/internal"
)

// GPU encoder detection cache
var (
	detectedEncoder     string
	detectedEncoderOnce sync.Once
)

// videoEncoder represents a video encoder configuration
type videoEncoder struct {
	name  string   // display name
	codec string   // ffmpeg codec name
	args  []string // additional encoder arguments
}

// availableEncoders lists GPU encoders in priority order, with CPU fallback last
var availableEncoders = []videoEncoder{
	// NVIDIA NVENC - use higher cq value for better compression (scale is 0-51, higher = smaller file)
	{"NVENC", "h264_nvenc", []string{"-preset", "p4", "-rc", "vbr", "-cq", "30", "-b:v", "0"}},
	// AMD AMF
	{"AMF", "h264_amf", []string{"-quality", "balanced", "-rc", "vbr_latency", "-qp_i", "28", "-qp_p", "28"}},
	// Intel Quick Sync
	{"QSV", "h264_qsv", []string{"-preset", "medium", "-global_quality", "28"}},
	// macOS VideoToolbox
	{"VideoToolbox", "h264_videotoolbox", []string{"-q:v", "65"}},
	// CPU fallback
	{"CPU", "libx264", []string{"-preset", "medium", "-crf", "23"}},
}

// detectEncoder finds the best available encoder
func detectEncoder() (videoEncoder, string) {
	for _, enc := range availableEncoders {
		// Test if encoder is available by running ffmpeg with it
		cmd := exec.Command("ffmpeg", "-hide_banner", "-f", "lavfi", "-i", "nullsrc=s=256x256:d=1", "-c:v", enc.codec, "-f", "null", "-")
		if err := cmd.Run(); err == nil {
			return enc, enc.name
		}
	}
	// Should not reach here since libx264 is always available if ffmpeg is installed
	return availableEncoders[len(availableEncoders)-1], "CPU"
}

// getEncoder returns the cached encoder or detects one
func getEncoder() videoEncoder {
	detectedEncoderOnce.Do(func() {
		enc, name := detectEncoder()
		detectedEncoder = name
		_ = enc // stored via name lookup
	})

	for _, enc := range availableEncoders {
		if enc.name == detectedEncoder {
			return enc
		}
	}
	return availableEncoders[len(availableEncoders)-1]
}

// CompressFile compresses a video file (.ts or .mp4) to .mkv format using ffmpeg in the background.
// Uses hardware GPU encoding if available, falls back to CPU (libx264).
// After successful compression, the original file is deleted.
func (ch *Channel) CompressFile(srcPath string) {
	go func() {
		ext := filepath.Ext(srcPath)
		mkvPath := strings.TrimSuffix(srcPath, ext) + ".mkv"
		srcFilename := filepath.Base(srcPath)
		mkvFilename := filepath.Base(mkvPath)

		// Get original file size
		srcInfo, err := os.Stat(srcPath)
		if err != nil {
			ch.Error("compress: failed to stat file: %s", err.Error())
			return
		}
		srcSize := srcInfo.Size()

		// Get the best available encoder
		encoder := getEncoder()

		ch.Info("compress: encoding %s (%s) using %s", srcFilename, internal.FormatFilesize(int(srcSize)), encoder.name)

		// Build ffmpeg command
		args := []string{"-y", "-i", srcPath, "-c:v", encoder.codec}
		args = append(args, encoder.args...)
		args = append(args, "-c:a", "aac", "-b:a", "128k", mkvPath)

		cmd := exec.Command("ffmpeg", args...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			ch.Error("compress: failed %s - %s", srcFilename, err.Error())
			if len(output) > 0 {
				// Only show last 500 chars of ffmpeg output to avoid flooding logs
				outStr := string(output)
				if len(outStr) > 500 {
					outStr = outStr[len(outStr)-500:]
				}
				ch.Error("compress: ffmpeg: %s", outStr)
			}
			return
		}

		// Get compressed file size
		mkvInfo, err := os.Stat(mkvPath)
		if err != nil {
			ch.Error("compress: failed to stat mkv: %s", err.Error())
			return
		}
		mkvSize := mkvInfo.Size()

		// Calculate compression ratio
		ratio := float64(mkvSize) / float64(srcSize) * 100

		// Delete the original file after successful compression
		if err := os.Remove(srcPath); err != nil {
			ch.Error("compress: failed to delete %s - %s", srcFilename, err.Error())
			return
		}

		ch.Info("compress: done %s -> %s (%s, %.1f%%)", srcFilename, mkvFilename, internal.FormatFilesize(int(mkvSize)), ratio)
	}()
}

// MuxAV combines separate video and audio source files into a single MP4 container.
func (ch *Channel) MuxAV(videoPath, audioPath, outputPath string) error {
	args := []string{
		"-y",
		"-i", videoPath,
		"-i", audioPath,
		"-map", "0:v:0",
		"-map", "1:a:0",
		"-c", "copy",
		"-copyts",
		"-avoid_negative_ts, "make_zero",
		outputPath,
	}

	cmd := exec.Command("ffmpeg", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if len(output) > 0 {
			outStr := string(output)
			if len(outStr) > 500 {
				outStr = outStr[len(outStr)-500:]
			}
			ch.Error("mux: ffmpeg: %s", outStr)
		}
		return fmt.Errorf("mux audio/video: %w", err)
	}

	ch.Info("mux: combined %s + %s -> %s", filepath.Base(videoPath), filepath.Base(audioPath), filepath.Base(outputPath))
	return nil
}
