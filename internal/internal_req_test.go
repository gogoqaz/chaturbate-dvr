package internal

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/teacat/chaturbate-dvr/entity"
	"github.com/teacat/chaturbate-dvr/server"
)

func TestGetBytesRejectsHTTPErrorBodies(t *testing.T) {
	previous := server.Config
	server.Config = &entity.Config{}
	t.Cleanup(func() { server.Config = previous })

	for _, status := range []int{http.StatusNotFound, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte("upstream error page"))
			}))
			defer srv.Close()

			body, err := NewReq().GetBytes(context.Background(), srv.URL)
			if err == nil || body != nil {
				t.Fatalf("GetBytes() = %q, %v; HTTP %d must not become recording data", body, err, status)
			}
			if !strings.Contains(err.Error(), http.StatusText(status)) {
				t.Fatalf("error = %v, want HTTP status for diagnosis", err)
			}
		})
	}
}

func TestGetBytesPreservesMediaAndProtectionErrors(t *testing.T) {
	previous := server.Config
	server.Config = &entity.Config{}
	t.Cleanup(func() { server.Config = previous })

	for _, tt := range []struct {
		name   string
		status int
		body   []byte
		want   error
	}{
		{"binary media", http.StatusOK, []byte{0, 0xff, 1, 0xfe}, nil},
		{"partial media", http.StatusPartialContent, []byte{0, 0xff}, nil},
		{"private stream", http.StatusForbidden, []byte("Forbidden"), ErrPrivateStream},
		{"Cloudflare", http.StatusServiceUnavailable, []byte("<title>Just a moment...</title>"), ErrCloudflareBlocked},
		{"age verification", http.StatusForbidden, []byte("Verify your age"), ErrAgeVerification},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write(tt.body)
			}))
			defer srv.Close()
			body, err := NewReq().GetBytes(context.Background(), srv.URL)
			if !errors.Is(err, tt.want) {
				t.Fatalf("GetBytes() error = %v, want %v", err, tt.want)
			}
			if tt.want == nil && !bytes.Equal(body, tt.body) {
				t.Fatalf("body = %v, want %v", body, tt.body)
			}
			if tt.want != nil && body != nil {
				t.Fatalf("protection error returned recording data: %q", body)
			}
		})
	}
}
