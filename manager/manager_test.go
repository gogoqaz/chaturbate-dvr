package manager

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/teacat/chaturbate-dvr/channel"
	"github.com/teacat/chaturbate-dvr/entity"
	"github.com/teacat/chaturbate-dvr/server"
)

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
	t.Chdir(t.TempDir())
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

// The Publisher goroutine every channel starts outlives the test that made it,
// so server.Manager is wired once for the whole binary rather than per test.
func init() {
	m, err := New()
	if err != nil {
		panic(err)
	}
	server.Manager = m
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()

	m, err := New()
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return m
}

func TestStartupRemuxRescansAfterQuietPeriod(t *testing.T) {
	m := newTestManager(t)
	ch := newRemuxTestChannel(t, time.Now().Add(-2*time.Minute+500*time.Millisecond))
	m.Channels.Store(ch.Config.Username, ch)
	done := make(chan struct{})
	go func() { m.remuxStartup(ch); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		ch.Stop()
		<-done
		t.Fatal("startup retry did not finish")
	}
	var logs []string
	for len(ch.LogCh) > 0 {
		logs = append(logs, <-ch.LogCh)
	}
	log := strings.Join(logs, "\n")
	skipped := strings.Index(log, "still being written")
	merged := strings.Index(log, "remux: merging")
	if skipped < 0 || merged <= skipped || strings.Count(log, "remux: merging") != 1 {
		t.Fatalf("startup must skip fresh files, then retry once quiet: %s", log)
	}
	if !ch.Config.IsPaused {
		t.Fatal("recovery must not resume a paused channel")
	}
}

func TestStartupRemuxCancelsRetryWhenDeleted(t *testing.T) {
	m := newTestManager(t)
	ch := newRemuxTestChannel(t, time.Now())
	m.Channels.Store(ch.Config.Username, ch)
	done := make(chan struct{})
	go func() { m.remuxStartup(ch); close(done) }()
	select {
	case log := <-ch.LogCh:
		if !strings.Contains(log, "still being written") {
			t.Fatalf("expected fresh-file skip: %s", log)
		}
	case <-time.After(time.Second):
		ch.Stop()
		<-done
		t.Fatal("initial scan did not run")
	}
	m.Channels.Delete(ch.Config.Username)
	ch.Stop()
	// A replacement with the same name must not inherit the old retry.
	m.Channels.Store(ch.Config.Username, newRemuxTestChannel(t, time.Now()))
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("deleting the channel must cancel its quiet-period timer")
	}
	for len(ch.LogCh) > 0 {
		if log := <-ch.LogCh; strings.Contains(log, "remux: merging") {
			t.Fatalf("deleted channel retried: %s", log)
		}
	}
}

func newRemuxTestChannel(t *testing.T, modifiedAt time.Time) *channel.Channel {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	base := dir + "/alice"
	for _, suffix := range []string{".video.mp4", ".audio.mp4"} {
		// Invalid media lets this test observe a retry without duplicating
		// the native-mux fixtures already covered in the channel package.
		if err := os.WriteFile(base+suffix, []byte("invalid media"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(base+suffix, modifiedAt, modifiedAt); err != nil {
			t.Fatal(err)
		}
	}
	return &channel.Channel{
		Config: &entity.ChannelConfig{Username: "alice", Pattern: base, IsPaused: true},
		LogCh:  make(chan string, 100), UpdateCh: make(chan bool, 100),
		CancelFunc: func() {}, PauseCancelFunc: func() {},
	}
}
