package chaturbate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/grafov/m3u8"
	"github.com/samber/lo"
	"github.com/teacat/chaturbate-dvr/internal"
	"github.com/teacat/chaturbate-dvr/server"
)

// Room status constants from the Chaturbate API.
const (
	StatusPublic  = "public"
	StatusPrivate = "private"
	StatusAway    = "away"
	StatusOffline = "offline"
)

// edgeRegionRegexp extracts edge region from URL like "edge14-sin.live.mmcdn.com"
var edgeRegionRegexp = regexp.MustCompile(`edge\d+-([a-z]+)`)

// edgeRegions is the list of CDN edge regions to try when geo-blocked
var edgeRegions = []string{"lax", "fra", "ams", "sin", "hnd"}

// APIResponse represents the response from /api/chatvideocontext/ endpoint
type APIResponse struct {
	HLSSource  string `json:"hls_source"`
	RoomStatus string `json:"room_status"`
}

// Client represents an API client for interacting with Chaturbate.
type Client struct {
	Req            *internal.Req
	LastRoomStatus string // cached from the most recent API call
}

// NewClient initializes and returns a new Client instance.
func NewClient() *Client {
	return &Client{
		Req: internal.NewReq(),
	}
}

// GetStream fetches the stream information for a given username.
// The room status is cached in Client.LastRoomStatus.
func (c *Client) GetStream(ctx context.Context, username string) (*Stream, error) {
	stream, roomStatus, err := FetchStream(ctx, c.Req, username)
	c.LastRoomStatus = roomStatus
	return stream, err
}

// GetRoomStatus returns the room status string (public, private, away, offline, etc.)
func (c *Client) GetRoomStatus(ctx context.Context, username string) (string, error) {
	resp, err := fetchAPIResponse(ctx, c.Req, username)
	if err != nil {
		return "", err
	}
	return resp.RoomStatus, nil
}

func fetchAPIResponse(ctx context.Context, client *internal.Req, username string) (*APIResponse, error) {
	apiURL := fmt.Sprintf("%sapi/chatvideocontext/%s/", server.Config.Domain, username)
	body, err := client.Get(ctx, apiURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get API response: %w", err)
	}

	var resp APIResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse API response: %w", err)
	}

	return &resp, nil
}

// FetchStream retrieves the streaming data using the Chaturbate API.
// Returns the stream, the room status string, and any error.
func FetchStream(ctx context.Context, client *internal.Req, username string) (*Stream, string, error) {
	resp, err := fetchAPIResponse(ctx, client, username)
	if err != nil {
		return nil, "", err
	}

	// Handle room status
	switch resp.RoomStatus {
	case StatusPrivate:
		return nil, resp.RoomStatus, internal.ErrPrivateStream
	case StatusAway, StatusOffline:
		return nil, resp.RoomStatus, internal.ErrChannelOffline
	}

	if resp.HLSSource == "" {
		return nil, resp.RoomStatus, internal.ErrChannelOffline
	}

	// Find working edge URL (geo-blocking fallback)
	workingURL, err := findWorkingEdgeURL(ctx, client, resp.HLSSource)
	if err != nil {
		return nil, resp.RoomStatus, err
	}

	return &Stream{HLSSource: workingURL}, resp.RoomStatus, nil
}

// findWorkingEdgeURL validates the HLS URL and tries alternative edge regions if geo-blocked.
func findWorkingEdgeURL(ctx context.Context, client *internal.Req, hlsSource string) (string, error) {
	// LL-HLS URLs use token-based sessions; HEAD requests consume the token
	// and cause subsequent GET requests to fail with "session_duplicated".
	// Skip HEAD validation for these URLs.
	if strings.Contains(hlsSource, "llhls.m3u8") {
		return hlsSource, nil
	}

	// 1. Validate original URL
	statusCode, err := client.Head(ctx, hlsSource)
	if err == nil && statusCode == 200 {
		return hlsSource, nil
	}

	// 2. Extract current region from URL
	matches := edgeRegionRegexp.FindStringSubmatch(hlsSource)
	if len(matches) < 2 {
		// URL doesn't match edge pattern, return original
		return hlsSource, nil
	}
	currentRegion := matches[1]

	// 3. Try alternative edge regions: lax, fra, ams, sin, hnd
	for _, region := range edgeRegions {
		if region == currentRegion {
			continue
		}
		altURL := strings.Replace(hlsSource, "-"+currentRegion+".", "-"+region+".", 1)

		statusCode, err := client.Head(ctx, altURL)
		if err == nil && statusCode == 200 {
			return altURL, nil
		}
	}

	return "", internal.ErrGeoBlocked
}

// Stream represents an HLS stream source.
type Stream struct {
	HLSSource string
}

// GetPlaylist retrieves the playlist corresponding to the given resolution and framerate.
func (s *Stream) GetPlaylist(ctx context.Context, resolution, framerate int) (*Playlist, error) {
	return FetchPlaylist(ctx, s.HLSSource, resolution, framerate)
}

// FetchPlaylist fetches and decodes the HLS playlist file.
func FetchPlaylist(ctx context.Context, hlsSource string, resolution, framerate int) (*Playlist, error) {
	if hlsSource == "" {
		return nil, errors.New("HLS source is empty")
	}

	resp, err := internal.NewReq().Get(ctx, hlsSource)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch HLS source: %w", err)
	}

	return ParsePlaylist(resp, hlsSource, resolution, framerate)
}

// ParsePlaylist decodes the M3U8 playlist and extracts the variant streams.
func ParsePlaylist(resp, hlsSource string, resolution, framerate int) (*Playlist, error) {
	p, _, err := m3u8.DecodeFrom(strings.NewReader(resp), true)
	if err != nil {
		return nil, fmt.Errorf("failed to decode m3u8 playlist: %w", err)
	}

	masterPlaylist, ok := p.(*m3u8.MasterPlaylist)
	if !ok {
		return nil, errors.New("invalid master playlist format")
	}

	return PickPlaylist(masterPlaylist, hlsSource, resolution, framerate)
}

// Playlist represents an HLS playlist containing variant streams.
type Playlist struct {
	PlaylistURL      string
	AudioPlaylistURL string
	RootURL          string
	Resolution       int
	Framerate        int
}

// Resolution represents a video resolution and its corresponding framerate.
type Resolution struct {
	Framerate    map[int]string // [framerate]url
	Width        int
	Alternatives []*m3u8.Alternative
}

// PickPlaylist selects the best matching variant stream based on resolution and framerate.
func PickPlaylist(masterPlaylist *m3u8.MasterPlaylist, baseURL string, resolution, framerate int) (*Playlist, error) {
	resolutions := map[int]*Resolution{}

	// Extract available resolutions and framerates from the master playlist
	for _, v := range masterPlaylist.Variants {
		parts := strings.Split(v.Resolution, "x")
		if len(parts) != 2 {
			continue
		}
		width, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("parse resolution: %w", err)
		}
		framerateVal := 30
		if v.FrameRate >= 59.0 || strings.Contains(v.Name, "FPS:60.0") {
			framerateVal = 60
		}
		if _, exists := resolutions[width]; !exists {
			resolutions[width] = &Resolution{Framerate: map[int]string{}, Width: width, Alternatives: v.Alternatives}
		}
		resolutions[width].Framerate[framerateVal] = v.URI
	}

	// Find exact match for requested resolution
	variant, exists := resolutions[resolution]
	if !exists {
		// Filter resolutions below the requested resolution
		candidates := lo.Filter(lo.Values(resolutions), func(r *Resolution, _ int) bool {
			return r.Width < resolution
		})
		// Pick the highest resolution among the candidates
		variant = lo.MaxBy(candidates, func(a, b *Resolution) bool {
			return a.Width > b.Width
		})
	}
	if variant == nil {
		return nil, fmt.Errorf("resolution not found")
	}

	var (
		finalResolution = variant.Width
		finalFramerate  = framerate
		audioPlaylist   string
	)
	// Select the desired framerate, or fallback to the first available framerate
	playlistURL, exists := variant.Framerate[framerate]
	if !exists {
		for fr, url := range variant.Framerate {
			playlistURL = url
			finalFramerate = fr
			break
		}
	}

	for _, alt := range variant.Alternatives {
		if alt == nil || alt.Type != "AUDIO" || alt.URI == "" {
			continue
		}
		audioPlaylist = resolveURL(baseURL, alt.URI)
		if alt.Default {
			break
		}
	}

	return &Playlist{
		PlaylistURL:      resolveURL(baseURL, playlistURL),
		AudioPlaylistURL: audioPlaylist,
		RootURL:          baseURL,
		Resolution:       finalResolution,
		Framerate:        finalFramerate,
	}, nil
}

// resolveURL resolves a potentially relative or absolute URI against a base URL.
func resolveURL(baseURL, ref string) string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return ref
	}
	refURL, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return base.ResolveReference(refURL).String()
}

// WatchHandler is a function type that processes video segments.
type WatchHandler func(b []byte, duration float64) error

// InitHandler is called once when an init segment (fMP4 moov atom) is detected.
type InitHandler func(initData []byte) error

// PollCompleteHandler is called once per poll cycle after both video and
// audio playlists have been processed. Used to coordinate side effects that
// must not interleave with segment processing (e.g. file rotation).
type PollCompleteHandler func() error

// WatchAVSegments continuously fetches and processes video segments, and optional separate audio segments.
func (p *Playlist) WatchAVSegments(ctx context.Context, handler WatchHandler, initHandler InitHandler, audioHandler WatchHandler, audioInitHandler InitHandler, pollComplete PollCompleteHandler) error {
	var (
		client           = internal.NewReq()
		lastSeq          = -1
		initWritten      = false
		audioLastSeq     = -1
		audioInitWritten = false
	)

	for {
		pollInterval, err := p.processMediaPlaylist(ctx, client, p.PlaylistURL, handler, initHandler, &lastSeq, &initWritten)
		if err != nil {
			return fmt.Errorf("video: %w", err)
		}
		if p.AudioPlaylistURL != "" {
			audioInterval, err := p.processMediaPlaylist(ctx, client, p.AudioPlaylistURL, audioHandler, audioInitHandler, &audioLastSeq, &audioInitWritten)
			if err != nil {
				return fmt.Errorf("audio: %w", err)
			}
			pollInterval = pickPollInterval(pollInterval, audioInterval)
		}

		if pollComplete != nil {
			if err := pollComplete(); err != nil {
				return fmt.Errorf("poll complete: %w", err)
			}
		}

		// Use the playlist's target duration as the polling interval (minimum 2s)
		// with random jitter to avoid synchronized requests across channels.
		if pollInterval < 2*time.Second {
			pollInterval = 2 * time.Second
		}
		jitter := time.Duration(rand.Intn(500)) * time.Millisecond
		timer := time.NewTimer(pollInterval + jitter)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func pickPollInterval(current, candidate time.Duration) time.Duration {
	if current <= 0 {
		return candidate
	}
	if candidate <= 0 {
		return current
	}
	if candidate < current {
		return candidate
	}
	return current
}

func (p *Playlist) processMediaPlaylist(ctx context.Context, client *internal.Req, playlistURL string, handler WatchHandler, initHandler InitHandler, lastSeq *int, initWritten *bool) (time.Duration, error) {
	resp, err := client.Get(ctx, playlistURL)
	if err != nil {
		return 0, fmt.Errorf("get playlist: %w", err)
	}
	pl, _, err := m3u8.DecodeFrom(strings.NewReader(resp), true)
	if err != nil {
		return 0, fmt.Errorf("decode from: %w", err)
	}
	playlist, ok := pl.(*m3u8.MediaPlaylist)
	if !ok {
		return 0, fmt.Errorf("cast to media playlist")
	}

	if !*initWritten && playlist.Map != nil && playlist.Map.URI != "" {
		initURL := resolveURL(playlistURL, playlist.Map.URI)
		initData, initErr := retry.DoWithData(
			func() ([]byte, error) {
				return client.GetBytes(ctx, initURL)
			},
			retry.Context(ctx),
			retry.Attempts(3),
			retry.Delay(600*time.Millisecond),
			retry.DelayType(retry.FixedDelay),
		)
		if initErr != nil {
			return 0, fmt.Errorf("fetch init segment: %w", initErr)
		}
		if initHandler != nil {
			if err := initHandler(initData); err != nil {
				return 0, fmt.Errorf("handler init: %w", err)
			}
		}
		*initWritten = true
	}

	for _, v := range playlist.Segments {
		if v == nil {
			continue
		}
		seq := internal.SegmentSeq(v.URI)
		if seq == -1 || seq <= *lastSeq {
			continue
		}

		segmentURL := resolveURL(playlistURL, v.URI)
		resp, err := retry.DoWithData(
			func() ([]byte, error) {
				return client.GetBytes(ctx, segmentURL)
			},
			retry.Context(ctx),
			retry.Attempts(3),
			retry.Delay(600*time.Millisecond),
			retry.DelayType(retry.FixedDelay),
		)
		if err != nil {
			break
		}
		if handler != nil {
			if err := handler(resp, v.Duration); err != nil {
				return 0, fmt.Errorf("handler: %w", err)
			}
		}
		*lastSeq = seq
	}

	return time.Duration(playlist.TargetDuration) * time.Second, nil
}
