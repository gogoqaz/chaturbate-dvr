package channel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/teacat/chaturbate-dvr/chaturbate"
	"github.com/teacat/chaturbate-dvr/internal"
	"github.com/teacat/chaturbate-dvr/server"
)

// Monitor starts monitoring the channel for live streams and records them.
func (ch *Channel) Monitor() {
	client := chaturbate.NewClient()
	ch.Info("starting to record `%s`", ch.Config.Username)

	// Create a new context with a cancel function,
	// the CancelFunc will be stored in the channel's CancelFunc field
	// and will be called by `Pause` or `Stop` functions
	ctx, _ := ch.WithCancel(context.Background())

	var err error
	for {
		if err = ctx.Err(); err != nil {
			break
		}

		pipeline := func() error {
			return ch.RecordStream(ctx, client)
		}
		onRetry := func(_ uint, err error) {
			ch.UpdateOnlineStatus(false)

			if errors.Is(err, internal.ErrChannelOffline) || errors.Is(err, internal.ErrPrivateStream) {
				if ctx.Err() == nil {
					ch.RoomStatus = client.GetRoomStatus(ctx, ch.Config.Username)
					ch.Update()
				}
				ch.Info("channel is %s, try again in %d min(s)", ch.RoomStatus, server.Config.Interval)
			} else if errors.Is(err, internal.ErrCloudflareBlocked) {
				ch.Info("channel was blocked by Cloudflare; try with `-cookies` and `-user-agent`? try again in %d min(s)", server.Config.Interval)
			} else if errors.Is(err, context.Canceled) {
				// ...
			} else {
				ch.Error("on retry: %s: retrying in %d min(s)", err.Error(), server.Config.Interval)
			}
		}
		if err = retry.Do(
			pipeline,
			retry.Context(ctx),
			retry.Attempts(0),
			retry.Delay(time.Duration(server.Config.Interval)*time.Minute),
			retry.DelayType(retry.FixedDelay),
			retry.OnRetry(onRetry),
		); err != nil {
			break
		}
	}

	// Always cleanup when monitor exits, regardless of error
	if err := ch.Cleanup(); err != nil {
		ch.Error("cleanup on monitor exit: %s", err.Error())
	}

	// Log error if it's not a context cancellation
	if err != nil && !errors.Is(err, context.Canceled) {
		ch.Error("record stream: %s", err.Error())
	}
}

// Update sends an update signal to the channel's update channel.
// This notifies the Server-sent Event to boradcast the channel information to the client.
func (ch *Channel) Update() {
	ch.UpdateCh <- true
}

// RecordStream records the stream of the channel using the provided client.
// It retrieves the stream information and starts watching the segments.
func (ch *Channel) RecordStream(ctx context.Context, client *chaturbate.Client) error {
	stream, err := client.GetStream(ctx, ch.Config.Username)
	if err != nil {
		return fmt.Errorf("get stream: %w", err)
	}
	playlist, err := stream.GetPlaylist(ctx, ch.Config.Resolution, ch.Config.Framerate)
	if err != nil {
		return fmt.Errorf("get playlist: %w", err)
	}

	ch.StreamedAt = time.Now().Unix()
	ch.Sequence = 0
	ch.InitSegment = nil
	ch.AudioInitSegment = nil
	ch.HasSeparateAudio = playlist.AudioPlaylistURL != ""

	if err := ch.NextFile(); err != nil {
		return fmt.Errorf("next file: %w", err)
	}

	// Ensure file is cleaned up when this function exits in any case
	defer func() {
		if err := ch.Cleanup(); err != nil {
			ch.Error("cleanup on record stream exit: %s", err.Error())
		}
	}()

	ch.RoomStatus = chaturbate.StatusPublic
	ch.UpdateOnlineStatus(true) // after GetPlaylist succeeds

	ch.Info("stream quality - resolution %dp (target: %dp), framerate %dfps (target: %dfps)", playlist.Resolution, ch.Config.Resolution, playlist.Framerate, ch.Config.Framerate)
	if ch.HasSeparateAudio {
		ch.Info("detected separate audio rendition, recording and muxing audio/video streams")
	}

	return playlist.WatchAVSegments(ctx, ch.HandleSegment, ch.HandleInitSegment, ch.HandleAudioSegment, ch.HandleAudioInitSegment)
}

// HandleInitSegment stores the fMP4 init segment and reopens the file with the correct extension.
func (ch *Channel) HandleInitSegment(initData []byte) error {
	ch.InitSegment = initData

	if ch.File == nil {
		return nil
	}

	oldName := ch.File.Name()
	if err := ch.File.Close(); err != nil {
		return fmt.Errorf("close file for rename: %w", err)
	}
	ch.File = nil

	newName := strings.TrimSuffix(oldName, filepath.Ext(oldName)) + ".mp4"
	if err := os.Rename(oldName, newName); err != nil {
		return fmt.Errorf("rename file to mp4: %w", err)
	}

	file, err := os.OpenFile(newName, os.O_APPEND|os.O_WRONLY, 0777)
	if err != nil {
		_ = os.Remove(newName)
		return fmt.Errorf("reopen file as mp4: %w", err)
	}
	ch.File = file

	n, err := ch.File.Write(initData)
	if err != nil {
		_ = ch.File.Close()
		ch.File = nil
		_ = os.Remove(newName)
		return fmt.Errorf("write init segment: %w", err)
	}
	ch.Filesize += n
	return nil
}

// HandleAudioInitSegment stores the fMP4 audio init segment and reopens the audio file with the correct extension.
func (ch *Channel) HandleAudioInitSegment(initData []byte) error {
	ch.AudioInitSegment = initData

	if ch.AudioFile == nil {
		return nil
	}

	oldName := ch.AudioFile.Name()
	if err := ch.AudioFile.Close(); err != nil {
		return fmt.Errorf("close audio file for rename: %w", err)
	}
	ch.AudioFile = nil

	newName := strings.TrimSuffix(oldName, filepath.Ext(oldName)) + ".mp4"
	if err := os.Rename(oldName, newName); err != nil {
		return fmt.Errorf("rename audio file to mp4: %w", err)
	}

	file, err := os.OpenFile(newName, os.O_APPEND|os.O_WRONLY, 0777)
	if err != nil {
		_ = os.Remove(newName)
		return fmt.Errorf("reopen audio file as mp4: %w", err)
	}
	ch.AudioFile = file

	if _, err := ch.AudioFile.Write(initData); err != nil {
		_ = ch.AudioFile.Close()
		ch.AudioFile = nil
		_ = os.Remove(newName)
		return fmt.Errorf("write audio init segment: %w", err)
	}
	return nil
}

// HandleSegment processes and writes segment data to a file.
func (ch *Channel) HandleSegment(b []byte, duration float64) error {
	if ch.Config.IsPaused {
		return retry.Unrecoverable(internal.ErrPaused)
	}

	n, err := ch.File.Write(b)
	if err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	ch.Filesize += n
	ch.Duration += duration
	ch.Info("duration: %s, filesize: %s", internal.FormatDuration(ch.Duration), internal.FormatFilesize(ch.Filesize))

	// Send an SSE update to update the view
	ch.Update()

	if ch.ShouldSwitchFile() {
		if err := ch.NextFile(); err != nil {
			return fmt.Errorf("next file: %w", err)
		}
		ch.Info("max filesize or duration exceeded, new file created: %s", ch.File.Name())
		return nil
	}
	return nil
}

// HandleAudioSegment processes and writes audio segment data to a sidecar file.
func (ch *Channel) HandleAudioSegment(b []byte, _ float64) error {
	if ch.AudioFile == nil {
		return nil
	}
	if ch.Config.IsPaused {
		return retry.Unrecoverable(internal.ErrPaused)
	}

	if _, err := ch.AudioFile.Write(b); err != nil {
		return fmt.Errorf("write audio file: %w", err)
	}
	return nil
}
