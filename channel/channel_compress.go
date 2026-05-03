package channel

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/teacat/chaturbate-dvr/internal"
)

// Track IDs for muxed output
const (
	videoTrackID uint32 = 1
	audioTrackID uint32 = 2
)

// GPU encoder detection cache
var (
	detectedEncoder     string
	detectedEncoderOnce sync.Once
)

// Frame-timing flag detection cache. ffmpeg 5+ uses -fps_mode (per-stream);
// 4.x only knows the legacy global -vsync. Probing once at runtime keeps
// CompressFile working on either generation without a static dependency.
var (
	fpsPassthroughFlag []string
	fpsPassthroughOnce sync.Once
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

// getFpsPassthroughFlag returns the ffmpeg flag(s) that pass each video
// frame's timestamp straight through to the muxer. Modern ffmpeg uses
// -fps_mode passthrough; older 4.x exits with "Unrecognized option
// 'fps_mode'", so we probe support once and fall back to -vsync passthrough
// (deprecated on 5+, but still functional through ffmpeg 8).
func getFpsPassthroughFlag() []string {
	fpsPassthroughOnce.Do(func() {
		cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "nullsrc=s=64x64:d=0.05",
			"-fps_mode", "passthrough", "-f", "null", "-")
		if err := cmd.Run(); err == nil {
			fpsPassthroughFlag = []string{"-fps_mode", "passthrough"}
		} else {
			fpsPassthroughFlag = []string{"-vsync", "passthrough"}
		}
	})
	return fpsPassthroughFlag
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

		// Preserve the recorded timeline while re-encoding. LL-HLS/fMP4
		// inputs often carry variable frame timing; letting ffmpeg
		// normalize frame cadence can make audio drift during compression.
		args := []string{"-y", "-copyts", "-start_at_zero", "-i", srcPath, "-c:v", encoder.codec}
		args = append(args, encoder.args...)
		args = append(args, getFpsPassthroughFlag()...)
		args = append(args, "-c:a", "aac", "-b:a", "128k", "-avoid_negative_ts", "make_zero", mkvPath)

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

		ch.MoveToOutputDir(mkvPath)
	}()
}

// MuxAV combines separate video and audio source files into a single MP4 container.
func (ch *Channel) MuxAV(videoPath, audioPath, outputPath string) error {
	// LL-HLS fragments are timestamped against an absolute presentation
	// timeline (TFDT), so the raw video and audio fragments only line up
	// if we preserve those timestamps with -copyts. Dropping -copyts made
	// ffmpeg renormalize each input to start at zero independently — which
	// is fine when the first fetched video/audio segments happened to
	// represent the same wall-clock moment, but when they differ (very
	// common on the very first poll of a live stream), the sound from the
	// later audio segment ends up playing against the earlier video
	// content, so users hear audio running seconds ahead of video.
	//
	// Keep -copyts for content alignment, -shortest so a stray partial
	// segment on one side cannot extend the combined duration past the
	// point both tracks have real samples, and -avoid_negative_ts
	// make_zero so H.264 B-frame reordering (negative DTS on the first
	// packet) cannot desync the output on strict players.
	args := []string{
		"-y",
		"-i", videoPath,
		"-i", audioPath,
		"-map", "0:v:0",
		"-map", "1:a:0",
		"-c", "copy",
		"-copyts",
		"-shortest",
		"-avoid_negative_ts", "make_zero",
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

// MuxAVNative combines separate fragmented MP4 audio/video tracks without ffmpeg.
func (ch *Channel) MuxAVNative(videoPath, audioPath, outputPath string) error {
	videoFile, err := mp4.ReadMP4File(videoPath)
	if err != nil {
		return fmt.Errorf("decode video mp4: %w", err)
	}
	audioFile, err := mp4.ReadMP4File(audioPath)
	if err != nil {
		return fmt.Errorf("decode audio mp4: %w", err)
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create mux output: %w", err)
	}
	defer outFile.Close()

	warn := func(msg string) { ch.Info("mux: %s", msg) }
	if err := writeCombinedFragmentedMP4(outFile, videoFile, audioFile, warn); err != nil {
		outFile.Close()
		os.Remove(outputPath)
		return fmt.Errorf("native mux audio/video: %w", err)
	}

	ch.Info("mux: combined %s + %s -> %s (native)", filepath.Base(videoPath), filepath.Base(audioPath), filepath.Base(outputPath))
	return nil
}

func writeCombinedFragmentedMP4(w io.Writer, videoFile, audioFile *mp4.File, warn func(string)) error {
	_, videoTrex, err := sourceTrack(videoFile, "vide")
	if err != nil {
		return fmt.Errorf("load video track: %w", err)
	}
	audioTrack, audioTrex, err := sourceTrack(audioFile, "soun")
	if err != nil {
		return fmt.Errorf("load audio track: %w", err)
	}

	// Combine fragments BEFORE reassigning track IDs — GetFullSamples
	// matches source traf boxes by trex.TrackID, which must still hold
	// the original value from the source file.
	videoFragments := collectFragments(videoFile)
	audioFragments := collectFragments(audioFile)
	if warn != nil && len(videoFragments) != len(audioFragments) {
		warn(fmt.Sprintf("fragment count mismatch (video=%d, audio=%d); output may have track-solo tail segments", len(videoFragments), len(audioFragments)))
	}
	segments, err := combineTrackFragments(videoFragments, videoTrex, audioFragments, audioTrex)
	if err != nil {
		return err
	}

	// Compute total media-timescale duration from the source fragments
	// while track IDs still match the original trex values, so the duration
	// hints written into mvhd/tkhd/mdhd reflect the real recorded length.
	// Without this, mvhd.Duration stays 0 (the value from a live init
	// segment), and players that read it as a hint instead of scanning every
	// fragment report the recording as much shorter than it is.
	videoMediaDur := sumFragmentDurations(videoFragments, videoTrex)
	audioMediaDur := sumFragmentDurations(audioFragments, audioTrex)

	ftyp := videoFile.Init.Ftyp
	moov := videoFile.Init.Moov
	if len(moov.Traks) != 1 || moov.Mvex == nil || len(moov.Mvex.Trexs) != 1 {
		return fmt.Errorf("expected single-track video init")
	}

	videoTrak := moov.Traks[0]
	if videoTrak.Mdia == nil || videoTrak.Mdia.Mdhd == nil {
		return fmt.Errorf("video track missing mdhd")
	}
	if audioTrack.Mdia == nil || audioTrack.Mdia.Mdhd == nil {
		return fmt.Errorf("audio track missing mdhd")
	}

	videoTrak.Tkhd.TrackID = videoTrackID
	moov.Mvex.Trexs[0].TrackID = videoTrackID

	audioTrack.Tkhd.TrackID = audioTrackID
	audioTrex.TrackID = audioTrackID

	moov.AddChild(audioTrack)
	moov.Mvex.AddChild(audioTrex)
	moov.Mvhd.NextTrackID = audioTrackID + 1

	movieTimescale := uint64(moov.Mvhd.Timescale)
	if movieTimescale == 0 {
		movieTimescale = 1
	}
	videoMdhdTimescale := uint64(videoTrak.Mdia.Mdhd.Timescale)
	audioMdhdTimescale := uint64(audioTrack.Mdia.Mdhd.Timescale)

	videoTrak.Mdia.Mdhd.Duration = videoMediaDur
	audioTrack.Mdia.Mdhd.Duration = audioMediaDur

	videoMovieDur := scaleDuration(videoMediaDur, movieTimescale, videoMdhdTimescale)
	audioMovieDur := scaleDuration(audioMediaDur, movieTimescale, audioMdhdTimescale)
	videoTrak.Tkhd.Duration = videoMovieDur
	audioTrack.Tkhd.Duration = audioMovieDur

	moov.Mvhd.Duration = videoMovieDur
	if audioMovieDur > moov.Mvhd.Duration {
		moov.Mvhd.Duration = audioMovieDur
	}

	out := mp4.NewFile()
	out.AddChild(ftyp, 0)
	out.AddChild(moov, ftyp.Size())
	for _, segment := range segments {
		out.AddMediaSegment(segment)
	}

	return out.Encode(w)
}

// sumFragmentDurations totals the trun durations of every fragment that
// belongs to the trex's track, falling back to the per-fragment or trex
// default sample duration when individual sample durations are absent.
func sumFragmentDurations(fragments []*mp4.Fragment, trex *mp4.TrexBox) uint64 {
	if trex == nil {
		return 0
	}
	var total uint64
	for _, frag := range fragments {
		if frag == nil || frag.Moof == nil {
			continue
		}
		for _, traf := range frag.Moof.Trafs {
			if traf == nil || traf.Tfhd == nil || traf.Tfhd.TrackID != trex.TrackID {
				continue
			}
			defaultDur := trex.DefaultSampleDuration
			if traf.Tfhd.HasDefaultSampleDuration() {
				defaultDur = traf.Tfhd.DefaultSampleDuration
			}
			for _, trun := range traf.Truns {
				if trun == nil {
					continue
				}
				total += trun.Duration(defaultDur)
			}
		}
	}
	return total
}

// scaleDuration converts duration from one timescale to another using
// integer math, guarding against zero divisors.
func scaleDuration(dur, dstTimescale, srcTimescale uint64) uint64 {
	if srcTimescale == 0 {
		return 0
	}
	return dur * dstTimescale / srcTimescale
}

func sourceTrack(file *mp4.File, handlerType string) (*mp4.TrakBox, *mp4.TrexBox, error) {
	if file == nil || file.Init == nil || file.Init.Moov == nil {
		return nil, nil, fmt.Errorf("missing init segment")
	}
	if len(file.Init.Moov.Traks) != 1 {
		return nil, nil, fmt.Errorf("expected exactly one track, got %d", len(file.Init.Moov.Traks))
	}

	trak := file.Init.Moov.Traks[0]
	if trak == nil || trak.Tkhd == nil || trak.Mdia == nil || trak.Mdia.Hdlr == nil {
		return nil, nil, fmt.Errorf("invalid track metadata")
	}
	if trak.Mdia.Hdlr.HandlerType != handlerType {
		return nil, nil, fmt.Errorf("expected %s track, got %s", handlerType, trak.Mdia.Hdlr.HandlerType)
	}
	if file.Init.Moov.Mvex == nil {
		return nil, nil, fmt.Errorf("missing mvex")
	}

	trex, ok := file.Init.Moov.Mvex.GetTrex(trak.Tkhd.TrackID)
	if !ok || trex == nil {
		return nil, nil, fmt.Errorf("missing trex for track %d", trak.Tkhd.TrackID)
	}

	return trak, trex, nil
}

func combineTrackFragments(videoFragments []*mp4.Fragment, videoTrex *mp4.TrexBox, audioFragments []*mp4.Fragment, audioTrex *mp4.TrexBox) ([]*mp4.MediaSegment, error) {
	maxFragments := len(videoFragments)
	if len(audioFragments) > maxFragments {
		maxFragments = len(audioFragments)
	}
	if maxFragments == 0 {
		return nil, fmt.Errorf("missing media fragments")
	}

	segments := make([]*mp4.MediaSegment, 0, maxFragments)
	for i := 0; i < maxFragments; i++ {
		trackIDs := make([]uint32, 0, 2)
		if i < len(videoFragments) {
			trackIDs = append(trackIDs, videoTrackID)
		}
		if i < len(audioFragments) {
			trackIDs = append(trackIDs, audioTrackID)
		}

		fragment, err := mp4.CreateMultiTrackFragment(uint32(i+1), trackIDs)
		if err != nil {
			return nil, fmt.Errorf("create fragment %d: %w", i, err)
		}

		if i < len(videoFragments) {
			if err := appendFragmentSamples(fragment, videoFragments[i], videoTrex, videoTrackID); err != nil {
				return nil, fmt.Errorf("append video fragment %d: %w", i, err)
			}
		}
		if i < len(audioFragments) {
			if err := appendFragmentSamples(fragment, audioFragments[i], audioTrex, audioTrackID); err != nil {
				return nil, fmt.Errorf("append audio fragment %d: %w", i, err)
			}
		}

		segment := mp4.NewMediaSegmentWithoutStyp()
		segment.AddFragment(fragment)
		segments = append(segments, segment)
	}

	return segments, nil
}

func appendFragmentSamples(dst, src *mp4.Fragment, trex *mp4.TrexBox, trackID uint32) error {
	fullSamples, err := src.GetFullSamples(trex)
	if err != nil {
		return err
	}
	for _, sample := range fullSamples {
		if err := dst.AddFullSampleToTrack(sample, trackID); err != nil {
			return err
		}
	}
	return nil
}

func collectFragments(file *mp4.File) []*mp4.Fragment {
	var fragments []*mp4.Fragment
	for _, segment := range file.Segments {
		fragments = append(fragments, segment.Fragments...)
	}
	return fragments
}
