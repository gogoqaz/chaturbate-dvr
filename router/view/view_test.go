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

func TestIndexIncludesLoadingAndActiveWorkCloseGuards(t *testing.T) {
	content, err := FS.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}

	html := string(content)
	for _, want := range []string{
		`id="global-loading"`,
		`htmx:beforeRequest`,
		`beforeunload`,
		`hasActiveWork`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("index template missing %q", want)
		}
	}
}

func TestChannelInfoExposesCompressingState(t *testing.T) {
	content, err := FS.ReadFile("templates/channel_info.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}

	html := string(content)
	for _, want := range []string{
		`data-compressing="{{ if .IsCompressing }}true{{ else }}false{{ end }}"`,
		`Compressing`,
		`loading-spinner`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("channel info template missing %q", want)
		}
	}
}
