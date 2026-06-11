package manager

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/teacat/chaturbate-dvr/channel"
	"github.com/teacat/chaturbate-dvr/entity"
	"github.com/teacat/chaturbate-dvr/server"
)

type noopManager struct{}

func (noopManager) CreateChannel(*entity.ChannelConfig, bool) error { return nil }
func (noopManager) StopChannel(string) error                        { return nil }
func (noopManager) PauseChannel(string) error                       { return nil }
func (noopManager) ResumeChannel(string) error                      { return nil }
func (noopManager) ChannelInfo() []*entity.ChannelInfo              { return nil }
func (noopManager) Publish(string, *entity.ChannelInfo)             {}
func (noopManager) Subscriber(http.ResponseWriter, *http.Request)   {}
func (noopManager) LoadConfig() error                               { return nil }
func (noopManager) SaveConfig() error                               { return nil }
func (noopManager) HasActiveWork() bool                             { return false }

func init() {
	server.Manager = noopManager{}
}

func TestPrepareLoadedConfigsRejectsDuplicateSanitizedUsernames(t *testing.T) {
	configs := []*entity.ChannelConfig{
		{Username: "Alice"},
		{Username: "alice"},
	}

	_, err := prepareLoadedConfigs(configs)

	if err == nil {
		t.Fatal("expected duplicate sanitized username error")
	}
	if !strings.Contains(err.Error(), `duplicate username after sanitize: "alice"`) {
		t.Fatalf("error = %q, want duplicate alice error", err.Error())
	}
}

func TestPrepareLoadedConfigsRejectsEmptySanitizedUsernames(t *testing.T) {
	configs := []*entity.ChannelConfig{
		{Username: "!!!"},
	}

	_, err := prepareLoadedConfigs(configs)

	if err == nil {
		t.Fatal("expected empty sanitized username error")
	}
	if !strings.Contains(err.Error(), "empty username after sanitize") {
		t.Fatalf("error = %q, want empty username error", err.Error())
	}
}

func TestLoadConfigRejectsDuplicateSanitizedUsernames(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
	if err := os.MkdirAll("./conf", 0777); err != nil {
		t.Fatalf("mkdir conf: %v", err)
	}
	if err := os.WriteFile("./conf/channels.json", []byte(`[
  {"username":"Alice"},
  {"username":"alice"}
]`), 0666); err != nil {
		t.Fatalf("write channels config: %v", err)
	}
	m, err := New()
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	err = m.LoadConfig()

	if err == nil {
		t.Fatal("expected duplicate sanitized username error")
	}
	if !strings.Contains(err.Error(), `duplicate username after sanitize: "alice"`) {
		t.Fatalf("error = %q, want duplicate alice error", err.Error())
	}
	if _, ok := m.Channels.Load("alice"); ok {
		t.Fatal("duplicate config should fail before storing channels")
	}
}

func TestHasActiveWorkDetectsRecordingOrCompressingChannels(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	recording := channel.New(&entity.ChannelConfig{Username: "alice"})
	recording.IsOnline = true
	compressing := channel.New(&entity.ChannelConfig{Username: "beth"})
	compressing.BeginCompression()
	paused := channel.New(&entity.ChannelConfig{Username: "cora", IsPaused: true})
	paused.IsOnline = true

	m.Channels.Store(recording.Config.Username, recording)
	m.Channels.Store(compressing.Config.Username, compressing)
	m.Channels.Store(paused.Config.Username, paused)

	if !m.HasActiveWork() {
		t.Fatal("HasActiveWork() = false, want true when channels are recording or compressing")
	}

	recording.IsOnline = false
	compressing.EndCompression()

	if m.HasActiveWork() {
		t.Fatal("HasActiveWork() = true, want false when only paused online channels remain")
	}
}
