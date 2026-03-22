# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build Commands

```bash
# Build for current platform
go build -o chaturbate-dvr

# Build with version (used by CI, optional locally)
go build -ldflags "-s -w -X main.version=v2.2.0" -o chaturbate-dvr

# Cross-compile for all platforms
GOOS=windows GOARCH=amd64 go build -o bin/x64_windows_chaturbate-dvr.exe &&
GOOS=darwin GOARCH=amd64 go build -o bin/x64_macos_chaturbate-dvr &&
GOOS=linux GOARCH=amd64 go build -o bin/x64_linux_chaturbate-dvr &&
GOOS=windows GOARCH=arm64 go build -o bin/arm64_windows_chaturbate-dvr.exe &&
GOOS=darwin GOARCH=arm64 go build -o bin/arm64_macos_chaturbate-dvr &&
GOOS=linux GOARCH=arm64 go build -o bin/arm64_linux_chaturbate-dvr

# Run locally
go run .

# Docker
docker build -t chaturbate-dvr .
docker-compose up
```

## Frontend Build

```bash
# Install dependencies (once)
npm install

# Build CSS (required after changing Tailwind classes in HTML templates)
npm run build:css

# Watch mode (auto-rebuild on HTML changes)
npm run dev:css
```

### Frontend Stack

- **Tailwind CSS v3** (npm build, output git-tracked at `router/view/templates/styles/app.css`)
- **HTMX + SSE** for real-time updates (unchanged)
- **Native `<dialog>`** for modals (replaced Tocas JS)
- **Inline SVGs** for icons (Feather/Lucide style)
- Templates embedded via `go:embed` -- must restart `go run .` to see template changes
- Master-detail layout: sidebar channel list + detail panel with info/logs

### Chaturbate API

- Endpoint: `{domain}/api/chatvideocontext/{username}/`
- `room_status` values: `public`, `private`, `group`, `away`, `offline`, `hidden`
- Thumbnail: `https://thumb.live.mmcdn.com/ri/{username}.jpg`
- Paused channels poll room status via `CheckOnlineWhilePaused()` goroutine

## Architecture

This is a Go application for recording HLS streams with a web-based UI. It operates in two modes:
- **Web UI mode**: When run without `-u` flag, starts a web server on port 8080
- **CLI mode**: When run with `-u <username>`, records a single channel directly

### Core Package Structure

- **main.go**: Entry point using `urfave/cli/v2` for CLI argument parsing
- **manager/**: Manages multiple channels via `sync.Map`, handles SSE (Server-Sent Events) for real-time UI updates
- **channel/**: Core recording logic - each channel runs in its own goroutine with context-based cancellation
  - `channel.go`: Channel state management, logging, SSE publishing
  - `channel_record.go`: Stream monitoring loop with retry logic
  - `channel_file.go`: File I/O for .ts video segments
- **chaturbate/**: API client for fetching stream info and HLS playlists, parses M3U8 format
- **router/**: Gin-based HTTP server with embedded static files and HTML templates
- **entity/**: Shared data structures (`ChannelConfig`, `ChannelInfo`, `Config`)
- **config/**: Configuration initialization from CLI flags
- **server/**: Global state holders (`Config`, `Manager`)
- **internal/**: HTTP request utilities and helpers

### Key Dependencies

- `gin-gonic/gin`: Web framework
- `grafov/m3u8`: HLS playlist parsing
- `r3labs/sse/v2`: Server-Sent Events for real-time UI updates
- `avast/retry-go/v4`: Retry logic for network operations

### Data Flow

1. User adds channel via Web UI or CLI flag
2. `Manager.CreateChannel()` creates a `Channel` and starts `ch.Monitor()` goroutine
3. `Monitor()` uses `chaturbate.Client` to fetch stream info and playlist
4. `Playlist.WatchSegments()` continuously fetches segments (.ts or .m4s) and passes to `HandleSegment()`
5. Segments are written to files with configurable naming patterns
6. Channel state updates broadcast via SSE to connected web clients

### Streaming Formats

Chaturbate uses two HLS formats (both must be supported):
- **Legacy HLS**: `playlist.m3u8`, relative URIs, `.ts` segments, no token
- **LL-HLS**: `llhls.m3u8?token=...`, absolute-path URIs, `.m4s` (fMP4) segments with init segment (EXT-X-MAP), token creates a session on first GET (HEAD consumes it)

### CI/CD

- `.github/workflows/release.yml` - push a `v*` tag to build binaries and create GitHub Release
- CI runs `npm ci && npm run build:css` before Go build
- Dockerfile uses multi-stage build (Node.js for CSS, then Go)
- Version is injected at build time via `-ldflags "-X main.version=<tag>"` (see `var version` in `main.go`)
- Local builds without `-ldflags` will show version as `dev`

### Configuration Persistence

Channels are saved to `./conf/channels.json` and auto-loaded on startup in Web UI mode.
