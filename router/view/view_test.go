package view

import (
	"strings"
	"testing"
)

func TestChannelListItemsUseKeyboardAccessibleButtons(t *testing.T) {
	content, err := FS.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}

	html := string(content)
	if strings.Contains(html, `<div class="channel-item`) {
		t.Fatal("channel list items should not be clickable divs")
	}
	if !strings.Contains(html, `<button type="button"`) ||
		!strings.Contains(html, `class="channel-item`) {
		t.Fatal("channel list items should be rendered as native buttons")
	}
}
