package chaturbate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/grafov/m3u8"
	"github.com/teacat/chaturbate-dvr/entity"
	"github.com/teacat/chaturbate-dvr/internal"
	"github.com/teacat/chaturbate-dvr/server"
)

func TestPickPlaylistIncludesDefaultAudioRendition(t *testing.T) {
	t.Parallel()

	master := &m3u8.MasterPlaylist{
		Variants: []*m3u8.Variant{
			{
				URI: "video.m3u8",
				VariantParams: m3u8.VariantParams{
					Resolution: "1920x1080",
					FrameRate:  60,
					Audio:      "audio-main",
					Alternatives: []*m3u8.Alternative{
						{Type: "AUDIO", GroupId: "audio-main", URI: "audio-en.m3u8", Name: "English", Default: true},
					},
				},
			},
		},
	}

	playlist, err := PickPlaylist(master, "https://example.com/master.m3u8", 1080, 60)
	if err != nil {
		t.Fatalf("PickPlaylist() error = %v", err)
	}
	if got, want := playlist.PlaylistURL, "https://example.com/video.m3u8"; got != want {
		t.Fatalf("PlaylistURL = %q, want %q", got, want)
	}
	if got, want := playlist.AudioPlaylistURL, "https://example.com/audio-en.m3u8"; got != want {
		t.Fatalf("AudioPlaylistURL = %q, want %q", got, want)
	}
}

// TestProcessMediaPlaylistKeepsLastSeqOnFetchFailure guards against silent
// segment drop: if a segment fetch fails, lastSeq must not advance past the
// failed segment, so the next playlist poll can retry it (or at minimum not
// claim it was successfully processed).
func TestProcessMediaPlaylistKeepsLastSeqOnFetchFailure(t *testing.T) {
	if server.Config == nil {
		server.Config = &entity.Config{}
	}

	playlistBody := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		"#EXT-X-TARGETDURATION:2",
		"#EXT-X-MEDIA-SEQUENCE:100",
		"#EXTINF:2.000,",
		"seg_1_100_video_abc.m4s",
		"#EXTINF:2.000,",
		"seg_2_101_video_abc.m4s",
		"#EXTINF:2.000,",
		"seg_3_102_video_abc.m4s",
		"",
	}, "\n")

	var handlerCalls int32

	mux := http.NewServeMux()
	mux.HandleFunc("/playlist.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(playlistBody))
	})
	mux.HandleFunc("/seg_1_100_video_abc.m4s", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("seg-100-data"))
	})
	mux.HandleFunc("/seg_2_101_video_abc.m4s", func(w http.ResponseWriter, _ *http.Request) {
		// Close the connection mid-response so the client gets a real fetch error.
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("hijacker not supported")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		_ = conn.Close()
	})
	mux.HandleFunc("/seg_3_102_video_abc.m4s", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("seg-102-data"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	pl := &Playlist{
		PlaylistURL: srv.URL + "/playlist.m3u8",
	}

	handler := func(_ []byte, _ float64) error {
		atomic.AddInt32(&handlerCalls, 1)
		return nil
	}

	lastSeq := -1
	initWritten := false
	_, err := pl.processMediaPlaylist(context.Background(), internal.NewReq(), pl.PlaylistURL, handler, nil, &lastSeq, &initWritten)
	if err != nil {
		t.Fatalf("processMediaPlaylist() error = %v", err)
	}

	if got := atomic.LoadInt32(&handlerCalls); got != 1 {
		t.Fatalf("handler called %d times, want 1 (should stop at first failure)", got)
	}
	if lastSeq != 100 {
		t.Fatalf("lastSeq = %d, want 100 (must not advance past failed segment 101)", lastSeq)
	}
}
