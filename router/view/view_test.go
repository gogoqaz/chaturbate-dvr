package view

import (
	"bytes"
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

func TestDiskUsageTemplateHandlesNilData(t *testing.T) {
	var b bytes.Buffer
	if err := DiskUsageTpl.ExecuteTemplate(&b, "disk_usage", nil); err != nil {
		t.Fatalf("ExecuteTemplate() error = %v", err)
	}
	if !strings.Contains(b.String(), "Disk status unavailable") {
		t.Fatal("disk usage template should render unavailable state for nil data")
	}
}

func TestSidebarContainsDiskStatusSSESwap(t *testing.T) {
	content, err := FS.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}

	html := string(content)
	for _, want := range []string{
		`sse-connect="/updates?stream=updates"`,
		`sse-swap="disk-status"`,
		`{{ template "disk_usage" .DiskUsage }}`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("index template missing %q", want)
		}
	}
}

func TestDiskUsageTemplateHasAccessibleMeterText(t *testing.T) {
	content, err := FS.ReadFile("templates/disk_usage.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}

	html := string(content)
	for _, want := range []string{
		`aria-label="Recording disk usage"`,
		`role="meter"`,
		`aria-valuemin="0"`,
		`aria-valuemax="100"`,
		`Recording Disk`,
		`Disk status unavailable`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("disk usage template missing %q", want)
		}
	}
}
