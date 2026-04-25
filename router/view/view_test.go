package view

import (
	"bytes"
	"strings"
	"testing"

	"github.com/teacat/chaturbate-dvr/entity"
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
		`sse-swap="` + entity.EventDiskStatus + `"`,
		`{{ template "disk_usage" .DiskUsage }}`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("index template missing %q", want)
		}
	}
}

func TestDiskUsageTemplateHasAccessibleMeterText(t *testing.T) {
	var b bytes.Buffer
	usage := &entity.DiskUsageInfo{
		Path:        "/recordings",
		UsedPercent: 42,
		Used:        "42 GB",
		Free:        "58 GB",
		FolderSize:  "3.00 KB",
	}
	if err := DiskUsageTpl.ExecuteTemplate(&b, "disk_usage", usage); err != nil {
		t.Fatalf("ExecuteTemplate() error = %v", err)
	}

	html := b.String()
	for _, want := range []string{
		`aria-label="Recording disk usage"`,
		`role="meter"`,
		`aria-valuemin="0"`,
		`aria-valuemax="100"`,
		`aria-valuenow="42"`,
		`Recording Disk`,
		`Healthy`,
		`42 GB used`,
		`58 GB free`,
		`Folder`,
		`3.00 KB`,
		`/recordings`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("rendered disk usage template missing %q", want)
		}
	}
}
