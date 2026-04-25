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
	"time"

	"github.com/r3labs/sse/v2"
	"github.com/teacat/chaturbate-dvr/channel"
	"github.com/teacat/chaturbate-dvr/entity"
	"github.com/teacat/chaturbate-dvr/router/view"
)

// Manager is responsible for managing channels and their states.
type Manager struct {
	Channels         sync.Map
	SSE              *sse.Server
	recordingDirs    sync.Map
	diskStatusMu     sync.Mutex
	diskStatusCtx    context.Context
	diskStatusCancel context.CancelFunc
	diskPublishMu    sync.Mutex
	lastDiskPublish  time.Time
}

const diskStatusPublishMinInterval = 5 * time.Second

// New initializes a new Manager instance with an SSE server.
func New() (*Manager, error) {

	server := sse.New()
	server.SplitData = true

	updateStream := server.CreateStream("updates")
	updateStream.AutoReplay = false

	m := &Manager{
		SSE: server,
	}
	return m, nil
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

	pausedSeq := 0
	seq := 0
	for _, conf := range config {
		ch := channel.New(conf)
		m.Channels.Store(conf.Username, ch)

		if ch.Config.IsPaused {
			ch.Info("channel was paused, waiting for resume")
			ctx, cancel := context.WithCancel(context.Background())
			ch.PauseCancelFunc = cancel
			go ch.CheckOnlineWhilePaused(ctx, pausedSeq)
			pausedSeq++
			continue
		}
		go ch.Resume(seq)
		seq++
	}
	return nil
}

// CreateChannel starts monitoring an M3U8 stream
func (m *Manager) CreateChannel(conf *entity.ChannelConfig, shouldSave bool) error {
	conf.Sanitize()
	ch := channel.New(conf)

	// prevent duplicate channels
	_, ok := m.Channels.Load(conf.Username)
	if ok {
		return fmt.Errorf("channel %s already exists", conf.Username)
	}
	m.Channels.Store(conf.Username, ch)

	go ch.Resume(0)

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

// ResumeChannel resumes the channel.
func (m *Manager) ResumeChannel(username string) error {
	thing, ok := m.Channels.Load(username)
	if !ok {
		return nil
	}
	thing.(*channel.Channel).Resume(0)

	if err := m.SaveConfig(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
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

// Publish sends an SSE event to the specified channel.
func (m *Manager) Publish(evt entity.Event, info *entity.ChannelInfo) {
	switch evt {
	case entity.EventUpdate:
		var b bytes.Buffer
		if err := view.InfoTpl.ExecuteTemplate(&b, "channel_info", info); err != nil {
			fmt.Println("Error executing template:", err)
			return
		}
		m.SSE.Publish("updates", &sse.Event{
			Event: []byte(info.Username + "-info"),
			Data:  b.Bytes(),
		})
	case entity.EventLog:
		m.SSE.Publish("updates", &sse.Event{
			Event: []byte(info.Username + "-log"),
			Data:  []byte(strings.Join(info.Logs, "\n")),
		})
	}
}

// PublishDiskStatus sends the latest disk usage status to the updates stream.
func (m *Manager) PublishDiskStatus() {
	if !m.shouldPublishDiskStatus(time.Now()) {
		return
	}

	var b bytes.Buffer
	if err := view.DiskUsageTpl.ExecuteTemplate(&b, "disk_usage", m.DiskUsageInfo()); err != nil {
		fmt.Println("Error executing disk usage template:", err)
		return
	}
	m.SSE.Publish("updates", &sse.Event{
		Event: []byte(entity.EventDiskStatus),
		Data:  b.Bytes(),
	})
}

// SetRecordingDir tracks the directory currently receiving recording writes.
func (m *Manager) SetRecordingDir(username, dir string) {
	if username == "" || dir == "" {
		return
	}
	m.recordingDirs.Store(username, dir)
}

// ClearRecordingDir removes a channel's active recording directory.
func (m *Manager) ClearRecordingDir(username string) {
	if username == "" {
		return
	}
	m.recordingDirs.Delete(username)
}

func (m *Manager) shouldPublishDiskStatus(now time.Time) bool {
	m.diskPublishMu.Lock()
	defer m.diskPublishMu.Unlock()

	if m.lastDiskPublish.IsZero() || now.Sub(m.lastDiskPublish) >= diskStatusPublishMinInterval {
		m.lastDiskPublish = now
		return true
	}
	return false
}

func (m *Manager) StartDiskStatusPublisher(interval time.Duration) {
	m.diskStatusMu.Lock()
	defer m.diskStatusMu.Unlock()

	if m.diskStatusCancel != nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.diskStatusCtx = ctx
	m.diskStatusCancel = cancel
	go m.publishDiskStatusEvery(ctx, interval)
}

func (m *Manager) StopDiskStatusPublisher() {
	m.diskStatusMu.Lock()
	cancel := m.diskStatusCancel
	m.diskStatusCtx = nil
	m.diskStatusCancel = nil
	m.diskStatusMu.Unlock()

	if cancel != nil {
		cancel()
	}
}

func (m *Manager) publishDiskStatusEvery(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.PublishDiskStatus()
		}
	}
}

// Subscriber handles SSE subscriptions for the specified channel.
func (m *Manager) Subscriber(w http.ResponseWriter, r *http.Request) {
	m.SSE.ServeHTTP(w, r)
}
