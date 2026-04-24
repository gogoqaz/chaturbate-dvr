package channel

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/teacat/chaturbate-dvr/server"
)

// Pattern holds the date/time and sequence information for the filename pattern
type Pattern struct {
	Username string
	Year     string
	Month    string
	Day      string
	Hour     string
	Minute   string
	Second   string
	Sequence int
}

// NextFile prepares the next file to be created, by cleaning up the last file and generating a new one
func (ch *Channel) NextFile() error {
	if err := ch.Cleanup(); err != nil {
		return err
	}
	filename, err := ch.GenerateFilename()
	if err != nil {
		return err
	}
	ch.CurrentFilename = filename
	if err := ch.CreateNewFile(filename); err != nil {
		return err
	}

	// Increment the sequence number for the next file
	ch.Sequence++
	return nil
}

// Cleanup cleans the file and resets it, called when the stream errors out or before next file was created.
func (ch *Channel) Cleanup() error {
	if ch.File == nil && ch.AudioFile == nil {
		return nil
	}
	currentFilename := ch.CurrentFilename

	defer func() {
		ch.File = nil
		ch.AudioFile = nil
		ch.CurrentFilename = ""
		ch.Filesize = 0
		ch.Duration = 0
	}()

	videoFilename, videoInfo, err := closeTrackedFile(ch.File)
	if err != nil {
		return err
	}
	audioFilename, audioInfo, err := closeTrackedFile(ch.AudioFile)
	if err != nil {
		return err
	}

	if ch.HasSeparateAudio {
		switch {
		case videoInfo == nil && audioInfo == nil:
			return nil
		case videoInfo == nil:
			ch.Info("mux: video track missing; preserving audio-only file %s", filepath.Base(audioFilename))
			if ch.Config.Compress {
				ch.CompressFile(audioFilename)
			} else {
				ch.MoveToOutputDir(audioFilename)
			}
			return nil
		case audioInfo == nil:
			ch.Info("mux: audio track missing; preserving video-only file %s", filepath.Base(videoFilename))
			if ch.Config.Compress {
				ch.CompressFile(videoFilename)
			} else {
				ch.MoveToOutputDir(videoFilename)
			}
			return nil
		}

		finalOutput := currentFilename + ".mp4"
		if err := ch.MuxAV(videoFilename, audioFilename, finalOutput); err != nil {
			ch.Info("mux: ffmpeg mux failed, trying native fallback: %s", err.Error())
			if nativeErr := ch.MuxAVNative(videoFilename, audioFilename, finalOutput); nativeErr != nil {
				return fmt.Errorf("mux audio/video: %w", nativeErr)
			}
		}

		// Sanity-check the muxed file before discarding the sidecars. If the
		// output is missing or implausibly small, keep the sidecars so the
		// user can recover manually (or rerun mux with external tools).
		if ok, reason := muxOutputLooksValid(finalOutput, videoInfo, audioInfo); !ok {
			ch.Error("mux: output looks corrupt (%s); keeping sidecars %s and %s", reason, filepath.Base(videoFilename), filepath.Base(audioFilename))
			_ = os.Remove(finalOutput)
			return nil
		}

		_ = os.Remove(videoFilename)
		_ = os.Remove(audioFilename)

		if ch.Config.Compress {
			ch.CompressFile(finalOutput)
		} else {
			ch.MoveToOutputDir(finalOutput)
		}
		return nil
	}

	if videoInfo != nil && videoInfo.Size() > 0 {
		if ch.Config.Compress {
			ch.CompressFile(videoFilename)
		} else {
			ch.MoveToOutputDir(videoFilename)
		}
	}

	return nil
}

// muxOutputLooksValid returns true if the muxed MP4 appears to contain most
// of the source bytes. `-c copy` just repackages, so the output should be
// within a reasonable fraction of the combined input size; anything much
// smaller means the muxer bailed out early and the sidecars are more
// valuable than the corrupt result.
func muxOutputLooksValid(outputPath string, videoInfo, audioInfo os.FileInfo) (bool, string) {
	finalInfo, err := os.Stat(outputPath)
	if err != nil {
		return false, fmt.Sprintf("stat: %s", err.Error())
	}
	if finalInfo.Size() == 0 {
		return false, "empty output"
	}
	inputSize := videoInfo.Size() + audioInfo.Size()
	if inputSize == 0 {
		return true, ""
	}
	if finalInfo.Size()*2 < inputSize {
		return false, fmt.Sprintf("output %d bytes, inputs %d bytes", finalInfo.Size(), inputSize)
	}
	return true, ""
}

// MoveToOutputDir relocates a finalized recording into server.Config.OutputDir.
// Errors are non-fatal: the recording is already safely written at srcPath.
func (ch *Channel) MoveToOutputDir(srcPath string) string {
	if server.Config == nil || server.Config.OutputDir == "" {
		return srcPath
	}

	destDir := server.Config.OutputDir
	if server.Config.PerModelFolder {
		destDir = filepath.Join(destDir, ch.Config.Username)
	}
	if err := os.MkdirAll(destDir, 0777); err != nil {
		ch.Error("output-dir: mkdir %s: %s", destDir, err.Error())
		return srcPath
	}

	destPath := uniqueDestPath(filepath.Join(destDir, filepath.Base(srcPath)))
	if err := moveFile(srcPath, destPath); err != nil {
		ch.Error("output-dir: move %s: %s", filepath.Base(srcPath), err.Error())
		return srcPath
	}
	ch.Info("output-dir: moved %s -> %s", filepath.Base(srcPath), destPath)
	return destPath
}

// uniqueDestPath returns path if it does not exist, otherwise appends
// " (n)" before the extension until an unused path is found. Gives up
// after 1000 tries and returns the last candidate.
func uniqueDestPath(path string) string {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return path
	}
	ext := filepath.Ext(path)
	base := path[:len(path)-len(ext)]
	for i := 1; i < 1000; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
	return fmt.Sprintf("%s (999)%s", base, ext)
}

func moveFile(src, dest string) error {
	if err := os.Rename(src, dest); err == nil {
		return nil
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0666)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dest)
		return err
	}
	// Sync before close so a crash between close and os.Remove(src) can't
	// leave a truncated destination alongside a deleted source.
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(dest)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dest)
		return err
	}
	return os.Remove(src)
}

// GenerateFilename creates a filename based on the configured pattern and the current timestamp
func (ch *Channel) GenerateFilename() (string, error) {
	var buf bytes.Buffer

	// Parse the filename pattern defined in the channel's config
	tpl, err := template.New("filename").Parse(ch.Config.Pattern)
	if err != nil {
		return "", fmt.Errorf("filename pattern error: %w", err)
	}

	// Get the current time based on the Unix timestamp when the stream was started
	t := time.Unix(ch.StreamedAt, 0)
	pattern := &Pattern{
		Username: ch.Config.Username,
		Sequence: ch.Sequence,
		Year:     t.Format("2006"),
		Month:    t.Format("01"),
		Day:      t.Format("02"),
		Hour:     t.Format("15"),
		Minute:   t.Format("04"),
		Second:   t.Format("05"),
	}

	if err := tpl.Execute(&buf, pattern); err != nil {
		return "", fmt.Errorf("template execution error: %w", err)
	}
	return buf.String(), nil
}

// CreateNewFile creates a new file for the channel using the given filename
func (ch *Channel) CreateNewFile(filename string) error {
	// Ensure the directory exists before creating the file
	if err := os.MkdirAll(filepath.Dir(filename), 0777); err != nil {
		return fmt.Errorf("mkdir all: %w", err)
	}

	videoPath := ch.videoPath(filename)
	file, err := os.OpenFile(videoPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0777)
	if err != nil {
		return fmt.Errorf("cannot open file: %s: %w", filename, err)
	}
	ch.File = file

	if len(ch.InitSegment) > 0 {
		n, err := ch.File.Write(ch.InitSegment)
		if err != nil {
			return fmt.Errorf("write init segment: %w", err)
		}
		ch.Filesize += n
	}

	if ch.HasSeparateAudio {
		audioPath := ch.audioPath(filename)
		audioFile, err := os.OpenFile(audioPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0777)
		if err != nil {
			_ = ch.File.Close()
			ch.File = nil
			return fmt.Errorf("cannot open audio file: %s: %w", filename, err)
		}
		ch.AudioFile = audioFile

		if len(ch.AudioInitSegment) > 0 {
			if _, err := ch.AudioFile.Write(ch.AudioInitSegment); err != nil {
				_ = ch.File.Close()
				_ = ch.AudioFile.Close()
				ch.File = nil
				ch.AudioFile = nil
				return fmt.Errorf("write audio init segment: %w", err)
			}
		}
	}

	return nil
}

func (ch *Channel) videoPath(filename string) string {
	if ch.HasSeparateAudio {
		ext := ".video.ts"
		if len(ch.InitSegment) > 0 {
			ext = ".video.mp4"
		}
		return filename + ext
	}

	ext := ".ts"
	if len(ch.InitSegment) > 0 {
		ext = ".mp4"
	}
	return filename + ext
}

func (ch *Channel) audioPath(filename string) string {
	ext := ".audio.ts"
	if len(ch.AudioInitSegment) > 0 {
		ext = ".audio.mp4"
	}
	return filename + ext
}

func closeTrackedFile(file *os.File) (string, os.FileInfo, error) {
	if file == nil {
		return "", nil, nil
	}

	filename := file.Name()
	if err := file.Sync(); err != nil && !errors.Is(err, os.ErrClosed) {
		return "", nil, fmt.Errorf("sync file: %w", err)
	}
	if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		return "", nil, fmt.Errorf("close file: %w", err)
	}

	fileInfo, err := os.Stat(filename)
	if err != nil && !os.IsNotExist(err) {
		return "", nil, fmt.Errorf("stat file delete zero file: %w", err)
	}
	if fileInfo != nil && fileInfo.Size() == 0 {
		if err := os.Remove(filename); err != nil {
			return "", nil, fmt.Errorf("remove zero file: %w", err)
		}
		fileInfo = nil
	}

	return filename, fileInfo, nil
}

// ShouldSwitchFile determines whether a new file should be created.
func (ch *Channel) ShouldSwitchFile() bool {
	maxFilesizeBytes := ch.Config.MaxFilesize * 1024 * 1024
	maxDurationSeconds := ch.Config.MaxDuration * 60

	return (ch.Duration >= float64(maxDurationSeconds) && ch.Config.MaxDuration > 0) ||
		(ch.Filesize >= maxFilesizeBytes && ch.Config.MaxFilesize > 0)
}
