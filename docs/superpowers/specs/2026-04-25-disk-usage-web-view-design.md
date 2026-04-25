# Disk Usage Web View - Design Spec

## Overview

Add an always-visible disk usage meter to the Web UI sidebar so users can see whether the active recording filesystem has enough free space. The feature is visual, not just text: it shows a progress bar, used/free numbers, the recording path, and a warning state.

The meter belongs to the global sidebar footer because disk pressure affects the whole recorder, not a single channel.

## Goals

- Show recording disk usage in the sidebar footer.
- Use the filesystem that contains the active recording path.
- When no channel is actively recording, estimate the default recording path from the current filename pattern.
- Warn when free space is at or below 10% or 20 GB.
- Keep the Web UI usable if disk status cannot be read.
- Update the meter while recording without requiring a page refresh.

## Non-Goals

- Scanning historical recordings per channel or per model.
- User-configurable warning thresholds.
- Automatically stopping recordings, deleting files, or changing output paths.
- Reworking the channel list or channel detail layout.

---

## Section 1: UI Behavior

The sidebar footer keeps the existing version/dark-mode controls and adds a compact `Recording Disk` meter above or alongside them, depending on available width.

The normal state shows:

- status label: `Healthy`
- horizontal usage bar
- used and free sizes
- resolved recording directory, truncated if long

The warning state switches to red styling and changes the status label to `Low Space`. Warning is true when either condition is met:

- free percentage is at or below 10%
- free bytes are at or below 20 GB

If disk status is unavailable, the meter shows `Disk status unavailable` with neutral styling. This must not hide channel controls or block recording actions.

Mobile behavior follows the sidebar layout. The meter remains compact and must not push the primary channel controls off screen.

---

## Section 2: Disk Path Selection

The meter reports the filesystem for the active/default recording path:

1. If any channel has an open recording file, use that file's directory.
2. If no channel is actively recording, resolve the directory implied by `server.Config.Pattern`.
3. If the pattern cannot be resolved, fall back to `videos/`.

The path shown in the UI should be a user-facing directory, not the exact current media file. For separate audio/video recordings, either sidecar path is acceptable because both are written under the same generated base path.

This feature intentionally ignores `--output-dir` for the first version. The goal is to warn about the disk being written during active recording, not archive storage after completion.

---

## Section 3: Backend Data Model

Add a small view model, tentatively `entity.DiskUsageInfo`, with fields for:

- path label
- total bytes and formatted total
- used bytes and formatted used
- free bytes and formatted free
- used percent
- warning boolean
- warning reason
- error text

The calculation helper should use `syscall.Statfs` on the resolved directory and convert raw block counts into byte totals. Formatting should reuse `internal.FormatFilesize` so values match existing file-size UI.

Threshold logic should be isolated in a small function so it can be unit tested without relying on the host filesystem.

---

## Section 4: SSE Data Flow

Disk status is a global Web UI event, not a channel-specific field.

Add a new template partial, tentatively `disk_usage.html`, and an SSE event name such as `disk-status`. The sidebar footer renders the partial on initial page load, then uses `sse-swap="disk-status"` to receive updates.

Updates should happen in two cases:

1. After recording segment writes, so disk usage changes while recording.
2. On a low-frequency background tick, about every 30 seconds, so idle pages and externally changed disk usage do not stay stale.

If `Statfs` fails, publish an unavailable state instead of returning an error to the recording pipeline.

---

## Section 5: Error Handling

Disk status failures are UI-only failures. They must not:

- stop a channel
- pause recording
- fail the `/` route
- prevent SSE channel info updates

The UI should show a compact unavailable state. Server logs may include the error if useful, but repeated failures should not flood logs every tick.

---

## Section 6: Testing

Required tests:

1. Warning threshold returns true for `free <= 10%`.
2. Warning threshold returns true for `free <= 20 GB`.
3. Warning threshold returns false when both thresholds are healthy.
4. Default recording directory is derived from the filename pattern when no active file exists.
5. Template output includes a `disk-status` SSE swap target and accessible text labels.
6. Statfs failure produces an unavailable view model, not a panic.

Manual verification:

- run `go test ./...`
- run `npm run build:css` if template classes require Tailwind output changes
- reload the Web UI and confirm the sidebar meter appears in desktop and mobile layouts

---

## Section 7: Implementation Scope

Expected files to change:

- `entity/entity.go`
- `manager/manager.go` or a focused helper under `manager/`
- `router/router_handler.go`
- `router/view/view.go`
- `router/view/templates/index.html`
- `router/view/templates/disk_usage.html`
- `router/view/view_test.go`
- `router/view/templates/styles/app.css` only if Tailwind regeneration is needed

The implementation should keep the channel detail panel unchanged except for any necessary shared template data wiring.
