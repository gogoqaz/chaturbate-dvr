package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/r3labs/sse/v2"
	"github.com/teacat/chaturbate-dvr/channel"
	"github.com/teacat/chaturbate-dvr/entity"
	"github.com/teacat/chaturbate-dvr/router/view"
	"github.com/teacat/chaturbate-dvr/server"
)

// Manager is responsible for managing channels and their states.
type Manager struct {
	Channels sync.Map
	SSE      *sse.Server
}

// New initializes a new Manager instance with an SSE server.
func New() (*Manager, error) {

	server := sse.New()
	server.SplitData = true

	updateStream := server.CreateStream("updates")
	updateStream.AutoReplay = false

	return &Manager{
		SSE: server,
	}, nil
}

// SaveConfig saves the current channels and state to a JSON file.
func (m *Manager) SaveConfig() error {
	var config []*entity.ChannelConfig

	m.Channels.Range(func(key, value any) bool {
		config = append(config, value.(*channel.Channel).Config)
		return true
	})

	b, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.MkdirAll("./conf", 0777); err != nil {
		return fmt.Errorf("mkdir all conf: %w", err)
	}
	if err := os.WriteFile("./conf/channels.json", b, 0777); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

// LoadConfig loads the channels from JSON and starts them.
func (m *Manager) LoadConfig() error {
	b, err := os.ReadFile("./conf/channels.json")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	var config []*entity.ChannelConfig
	if err := json.Unmarshal(b, &config); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}
	config, err = prepareLoadedConfigs(config)
	if err != nil {
		return err
	}
	for _, conf := range config {
		if _, exists := m.Channels.Load(conf.Username); exists {
			return fmt.Errorf("duplicate username after sanitize: %q", conf.Username)
		}
	}

	// Register every channel before any starts, so the remux ownership check
	// sees the whole set rather than a half-populated map.
	channels := make([]*channel.Channel, 0, len(config))
	for _, conf := range config {
		ch := channel.New(conf)
		m.Channels.Store(conf.Username, ch)
		channels = append(channels, ch)
	}

	pausedSeq := 0
	seq := 0
	for _, ch := range channels {
		if ch.Config.IsPaused {
			m.autoRemux(ch, nil)
			ch.Info("channel was paused, waiting for resume")
			ctx, cancel := context.WithCancel(context.Background())
			ch.PauseCancelFunc = cancel
			go ch.CheckOnlineWhilePaused(ctx, pausedSeq)
			pausedSeq++
			continue
		}
		resumeSeq := seq
		m.autoRemux(ch, func() { ch.Resume(resumeSeq) })
		seq++
	}
	return nil
}

func prepareLoadedConfigs(configs []*entity.ChannelConfig) ([]*entity.ChannelConfig, error) {
	seen := make(map[string]struct{}, len(configs))
	for _, conf := range configs {
		if conf == nil {
			return nil, fmt.Errorf("empty channel config")
		}
		conf.Sanitize()
		if conf.Username == "" {
			return nil, fmt.Errorf("empty username after sanitize")
		}
		if _, ok := seen[conf.Username]; ok {
			return nil, fmt.Errorf("duplicate username after sanitize: %q", conf.Username)
		}
		seen[conf.Username] = struct{}{}
	}
	return configs, nil
}

// CreateChannel starts monitoring an M3U8 stream
func (m *Manager) CreateChannel(conf *entity.ChannelConfig, shouldSave bool) error {
	conf.Sanitize()

	// Fast path: reject an already-known duplicate before channel.New, so the
	// common re-add case never spawns the Publisher goroutine that New starts.
	if _, ok := m.Channels.Load(conf.Username); ok {
		return fmt.Errorf("channel %s already exists", conf.Username)
	}
	ch := channel.New(conf)
	// Store atomically to close the race where two concurrent creates of the
	// same username both pass the Load above; the loser cleans up its channel.
	if _, loaded := m.Channels.LoadOrStore(conf.Username, ch); loaded {
		ch.Stop()
		return fmt.Errorf("channel %s already exists", conf.Username)
	}

	m.autoRemux(ch, func() { ch.Resume(0) })

	if shouldSave {
		if err := m.SaveConfig(); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
	}
	return nil
}

// StopChannel stops the channel.
func (m *Manager) StopChannel(username string) error {
	thing, ok := m.Channels.Load(username)
	if !ok {
		return nil
	}
	thing.(*channel.Channel).Stop()
	m.Channels.Delete(username)

	if err := m.SaveConfig(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// PauseChannel pauses the channel.
func (m *Manager) PauseChannel(username string) error {
	thing, ok := m.Channels.Load(username)
	if !ok {
		return nil
	}
	thing.(*channel.Channel).Pause()

	if err := m.SaveConfig(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// ResumeChannel resumes the channel if it is paused.
//
// Resuming an already-active channel is a no-op: calling Resume on a running
// channel would spawn a second Monitor goroutine. ResumeIfPaused performs the
// check-and-resume atomically, so this is safe to call concurrently and when
// re-adding a channel that already exists.
func (m *Manager) ResumeChannel(username string) error {
	thing, ok := m.Channels.Load(username)
	if !ok {
		return nil
	}
	if !thing.(*channel.Channel).ResumeIfPaused(0) {
		return nil
	}

	if err := m.SaveConfig(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// RemuxChannel merges leftover audio/video sidecars in the background, and
// reports progress through the channel log.
func (m *Manager) RemuxChannel(username string) error {
	thing, ok := m.Channels.Load(username)
	if !ok {
		return nil
	}
	ch := thing.(*channel.Channel)
	go func() {
		if !m.mayRemux(ch) {
			return
		}
		if _, err := ch.RemuxOrphans(); err != nil {
			ch.Error("remux: %s", err.Error())
		}
	}()
	return nil
}

// autoRemux repairs recordings orphaned by a failed merge or by a crash
// mid-stream, then runs next. The scan must finish first: a new recording can
// reuse the same base name, and the scan would delete the file it appends to.
func (m *Manager) autoRemux(ch *channel.Channel, next func()) {
	go func() {
		if server.Config != nil && server.Config.AutoRemux && m.mayRemux(ch) {
			if _, err := ch.RemuxOrphansQuiet(); err != nil {
				ch.Error("remux: %s", err.Error())
			}
		}
		if next != nil {
			next()
		}
	}()
}

// mayRemux refuses to scan when another channel's recordings would match the
// same filenames, because both would then claim -- and delete -- the same pair.
func (m *Manager) mayRemux(ch *channel.Channel) bool {
	var conflict *channel.Channel
	m.Channels.Range(func(_, value any) bool {
		if other := value.(*channel.Channel); ch.ConflictsWith(other) {
			conflict = other
			return false
		}
		return true
	})
	if conflict == nil {
		return true
	}
	ch.Info("remux: skipped, %s records to filenames this pattern cannot be told apart from", conflict.Config.Username)
	return false
}

// ChannelInfo returns a list of channel information for the web UI.
func (m *Manager) ChannelInfo() []*entity.ChannelInfo {
	var channels []*entity.ChannelInfo

	// Iterate over the channels and append their information to the slice
	m.Channels.Range(func(key, value any) bool {
		channels = append(channels, value.(*channel.Channel).ExportInfo())
		return true
	})

	sort.Slice(channels, func(i, j int) bool {
		// Paused channels always sort to the bottom.
		getPriority := func(c *entity.ChannelInfo) int {
			switch {
			case !c.IsPaused && c.IsOnline:
				return 0 // Recording
			case !c.IsPaused:
				return 1 // Offline, actively watching
			case c.IsOnline:
				return 2 // Paused, currently online
			default:
				return 3 // Paused, offline
			}
		}

		pi, pj := getPriority(channels[i]), getPriority(channels[j])
		if pi != pj {
			return pi < pj
		}
		// Same priority: sort by username alphabetically
		return strings.ToLower(channels[i].Username) < strings.ToLower(channels[j].Username)
	})

	return channels
}

// Publish sends an SSE event to subscribers of the "updates" stream.
//
// It uses TryPublish (non-blocking) rather than Publish: SSE carries only
// ephemeral UI updates, so when the stream buffer is full the event is dropped
// and the next successful publish re-syncs the client. This decoupling is
// critical because Publish runs on the recording hot path
// (Channel.Publisher -> Info/Update -> HandleSegment). A blocking publish there
// lets a single slow or stalled SSE client (e.g. a browser tab suspended when a
// laptop lid closes while the TCP connection stays half-open) apply backpressure
// all the way back into the recording loop, stalling every channel until the
// client disconnects. See issue #34.
func (m *Manager) Publish(evt entity.Event, info *entity.ChannelInfo) {
	switch evt {
	case entity.EventUpdate:
		var b bytes.Buffer
		if err := view.InfoTpl.ExecuteTemplate(&b, "channel_info", info); err != nil {
			fmt.Println("Error executing template:", err)
			return
		}
		m.SSE.TryPublish("updates", &sse.Event{
			Event: []byte(info.Username + "-info"),
			Data:  b.Bytes(),
		})
	case entity.EventLog:
		m.SSE.TryPublish("updates", &sse.Event{
			Event: []byte(info.Username + "-log"),
			Data:  []byte(strings.Join(info.Logs, "\n")),
		})
	}
}

// Subscriber handles SSE subscriptions for the specified channel.
func (m *Manager) Subscriber(w http.ResponseWriter, r *http.Request) {
	m.SSE.ServeHTTP(w, r)
}
