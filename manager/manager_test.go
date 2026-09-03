package manager

import (
	"os"
	"strings"
	"testing"

	"github.com/teacat/chaturbate-dvr/channel"
	"github.com/teacat/chaturbate-dvr/entity"
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

func TestMayRemuxRejectsAnAmbiguousPatternSharedByChannels(t *testing.T) {
	const ambiguous = "videos/{{.Year}}-{{.Month}}-{{.Day}}_{{.Hour}}-{{.Minute}}-{{.Second}}"

	m, err := New()
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	alice := channel.New(&entity.ChannelConfig{Username: "alice", Pattern: ambiguous})
	m.Channels.Store("alice", alice)

	// The only channel on disk owns whatever it finds.
	if !m.mayRemux(alice) {
		t.Fatal("a lone channel must still be allowed to remux")
	}

	m.Channels.Store("bob", channel.New(&entity.ChannelConfig{Username: "bob", Pattern: ambiguous}))
	if m.mayRemux(alice) {
		t.Fatal("channels sharing a username-less pattern must not claim each other's files")
	}

	specific := channel.New(&entity.ChannelConfig{Username: "carol", Pattern: "videos/{{.Username}}_{{.Year}}"})
	m.Channels.Store("carol", specific)
	if !m.mayRemux(specific) {
		t.Fatal("a pattern carrying the username is unambiguous")
	}
}
