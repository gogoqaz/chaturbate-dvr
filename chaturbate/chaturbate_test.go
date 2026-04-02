package chaturbate

import (
	"testing"

	"github.com/grafov/m3u8"
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
