package entity

import "testing"

func TestChannelConfigSanitizeLowercasesUsername(t *testing.T) {
	conf := &ChannelConfig{Username: "Alice-01!"}

	conf.Sanitize()

	if conf.Username != "alice-01" {
		t.Fatalf("username = %q, want %q", conf.Username, "alice-01")
	}
}
