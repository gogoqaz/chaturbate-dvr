package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/teacat/chaturbate-dvr/entity"
	"github.com/teacat/chaturbate-dvr/internal"
	"github.com/teacat/chaturbate-dvr/server"
)

// Channel represents a channel instance.
type Channel struct {
	CancelFunc      context.CancelFunc
	PauseCancelFunc context.CancelFunc // cancels the paused online-check goroutine
	LogCh           chan string
	UpdateCh        chan bool

	IsOnline   bool
	RoomStatus string // public, private, group, away, offline
	StreamedAt int64
	Duration   float64 // Seconds
	Filesize   int     // Bytes
	Sequence   int

	Logs []string

	File   *os.File
	Config *entity.ChannelConfig
}

// New creates a new channel instance with the given manager and configuration.
func New(conf *entity.ChannelConfig) *Channel {
	ch := &Channel{
		LogCh:           make(chan string),
		UpdateCh:        make(chan bool),
		Config:          conf,
		CancelFunc:      func() {},
		PauseCancelFunc: func() {},
	}
	go ch.Publisher()

	return ch
}

// Publisher listens for log messages and updates from the channel
// and publishes once received.
func (ch *Channel) Publisher() {
	for {
		select {
		case v := <-ch.LogCh:
			// Append the log message to ch.Logs and keep only the last 100 rows
			ch.Logs = append(ch.Logs, v)
			if len(ch.Logs) > 100 {
				ch.Logs = ch.Logs[len(ch.Logs)-100:]
			}
			server.Manager.Publish(entity.EventLog, ch.ExportInfo())

		case <-ch.UpdateCh:
			server.Manager.Publish(entity.EventUpdate, ch.ExportInfo())
		}
	}
}

// WithCancel creates a new context with a cancel function,
// then stores the cancel function in the channel's CancelFunc field.
//
// This is used to cancel the context when the channel is stopped or paused.
func (ch *Channel) WithCancel(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, ch.CancelFunc = context.WithCancel(ctx)
	return ctx, ch.CancelFunc
}

// Info logs an informational message.
func (ch *Channel) Info(format string, a ...any) {
	ch.LogCh <- fmt.Sprintf("%s [INFO] %s", time.Now().Format("15:04"), fmt.Sprintf(format, a...))
	log.Printf(" INFO [%s] %s", ch.Config.Username, fmt.Sprintf(format, a...))
}

// Error logs an error message.
func (ch *Channel) Error(format string, a ...any) {
	ch.LogCh <- fmt.Sprintf("%s [ERROR] %s", time.Now().Format("15:04"), fmt.Sprintf(format, a...))
	log.Printf("ERROR [%s] %s", ch.Config.Username, fmt.Sprintf(format, a...))
}

// ExportInfo exports the channel information as a ChannelInfo struct.
func (ch *Channel) ExportInfo() *entity.ChannelInfo {
	var filename string
	if ch.File != nil {
		filename = ch.File.Name()
	}
	var streamedAt string
	if ch.StreamedAt != 0 {
		streamedAt = time.Unix(ch.StreamedAt, 0).Format("2006-01-02 15:04 AM")
	}
	return &entity.ChannelInfo{
		IsOnline:     ch.IsOnline,
		IsPaused:     ch.Config.IsPaused,
		RoomStatus:   ch.RoomStatus,
		Username:     ch.Config.Username,
		MaxDuration:  internal.FormatDuration(float64(ch.Config.MaxDuration * 60)), // MaxDuration from config is in minutes
		MaxFilesize:  internal.FormatFilesize(ch.Config.MaxFilesize * 1024 * 1024), // MaxFilesize from config is in MB
		StreamedAt:   streamedAt,
		CreatedAt:    ch.Config.CreatedAt,
		Duration:     internal.FormatDuration(ch.Duration),
		Filesize:     internal.FormatFilesize(ch.Filesize),
		Filename:     filename,
		Logs:         ch.Logs,
		GlobalConfig: server.Config,
	}
}

// Pause pauses the channel and cancels the context.
func (ch *Channel) Pause() {
	// Stop the monitoring loop, this also updates `ch.IsOnline` to false
	// `context.Canceled` → `ch.Monitor()` → `onRetry` → `ch.UpdateOnlineStatus(false)`.
	ch.CancelFunc()

	ch.Config.IsPaused = true
	ch.Update()
	ch.Info("channel paused")

	// Start a lightweight goroutine to periodically check if the channel is online
	go ch.CheckOnlineWhilePaused(0)
}

// Stop stops the channel and cancels the context.
func (ch *Channel) Stop() {
	// Stop the monitoring loop
	ch.CancelFunc()
	ch.PauseCancelFunc() // stop the online-check goroutine if running

	ch.Info("channel stopped")
}

// Resume resumes the channel monitoring.
//
// `startSeq` is used to prevent all channels from starting at the same time, preventing TooManyRequests errors.
// It's only be used when program starting and trying to resume all channels at once.
func (ch *Channel) Resume(startSeq int) {
	ch.PauseCancelFunc() // stop the online-check goroutine
	ch.Config.IsPaused = false

	ch.Update()
	ch.Info("channel resumed")

	<-time.After(time.Duration(startSeq) * time.Second)
	go ch.Monitor()
}

// UpdateOnlineStatus updates the online status of the channel.
func (ch *Channel) UpdateOnlineStatus(isOnline bool) {
	ch.IsOnline = isOnline
	ch.Update()
}

// CheckOnlineWhilePaused periodically checks if the channel is online while paused.
// startSeq staggers the initial check: waits startSeq*5 seconds before first check,
// then continues checking every Interval minutes.
func (ch *Channel) CheckOnlineWhilePaused(startSeq int) {
	ctx, cancel := context.WithCancel(context.Background())
	ch.PauseCancelFunc = cancel

	client := internal.NewReq()

	// Stagger initial check by 5 seconds per channel
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Duration(startSeq*5) * time.Second):
	}

	// Do the first check immediately, then loop on interval
	for {
		ch.checkOnlineStatus(ctx, client)

		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(server.Config.Interval) * time.Minute):
		}
	}
}

// checkOnlineStatus calls the Chaturbate API to check room status.
func (ch *Channel) checkOnlineStatus(ctx context.Context, client *internal.Req) {
	apiURL := fmt.Sprintf("%sapi/chatvideocontext/%s/", server.Config.Domain, ch.Config.Username)
	body, err := client.Get(ctx, apiURL)
	if err != nil {
		return
	}
	var resp struct {
		RoomStatus string `json:"room_status"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return
	}
	isOnline := resp.RoomStatus != "away" && resp.RoomStatus != "offline" && resp.RoomStatus != ""
	statusChanged := ch.IsOnline != isOnline || ch.RoomStatus != resp.RoomStatus
	ch.IsOnline = isOnline
	ch.RoomStatus = resp.RoomStatus
	if statusChanged {
		ch.Info("channel status: %s (paused)", resp.RoomStatus)
		ch.Update()
	}
}
