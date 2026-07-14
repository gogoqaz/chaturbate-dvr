package manager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/r3labs/sse/v2"
	"github.com/teacat/chaturbate-dvr/entity"
)

// stalledResponseWriter is an http.ResponseWriter + http.Flusher whose Write
// blocks until release is closed. It simulates an SSE client that has stopped
// reading its response body (e.g. a browser tab suspended when a laptop lid
// closes while the TCP connection stays half-open), which is what stalls the
// SSE delivery goroutine in the real failure (issue #34).
type stalledResponseWriter struct {
	header     http.Header
	release    chan struct{}
	firstWrite chan struct{}
	once       sync.Once
}

func newStalledResponseWriter() *stalledResponseWriter {
	return &stalledResponseWriter{
		header:     make(http.Header),
		release:    make(chan struct{}),
		firstWrite: make(chan struct{}),
	}
}

func (w *stalledResponseWriter) Header() http.Header { return w.header }
func (w *stalledResponseWriter) WriteHeader(int)     {}
func (w *stalledResponseWriter) Flush()              {}

func (w *stalledResponseWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.firstWrite) })
	<-w.release
	return len(p), nil
}

// TestPublishDoesNotBlockOnStalledSubscriber guards against issue #34: a slow or
// stalled SSE client must never apply backpressure to Manager.Publish. Publish
// runs on the recording hot path, so a blocking publish stalls recording for
// every channel until the client disconnects. With the blocking sse.Publish this
// test deadlocks; with the non-blocking sse.TryPublish it passes.
func TestPublishDoesNotBlockOnStalledSubscriber(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	// A cancelable context lets us deregister the subscriber on cleanup so the
	// SSE handler goroutine can exit instead of leaking.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newStalledResponseWriter()
	req := httptest.NewRequest(http.MethodGet, "/updates?stream=updates", nil).WithContext(ctx)
	go m.Subscriber(w, req)

	info := &entity.ChannelInfo{Username: "tester", Logs: []string{"hello"}}

	// The SSE handler registers its subscriber asynchronously, so an event
	// published before registration is dropped. Keep publishing until the
	// handler pulls one from the subscriber's connection channel and blocks
	// inside Write: once firstWrite fires the subscriber is registered and its
	// delivery path is stalled.
	stalled := false
	deadline := time.Now().Add(3 * time.Second)
	for !stalled && time.Now().Before(deadline) {
		m.Publish(entity.EventLog, info)
		select {
		case <-w.firstWrite:
			stalled = true
		case <-time.After(20 * time.Millisecond):
		}
	}
	if !stalled {
		t.Fatal("SSE handler never wrote; subscriber did not register/stall as expected")
	}

	// Flood past the SSE stream buffer plus the per-subscriber connection buffer
	// (hardcoded to 64 in r3labs/sse). Beyond that a blocking publish deadlocks;
	// TryPublish drops events and every call returns immediately.
	const perSubscriberBuffer = 64
	floodCount := sse.DefaultBufferSize + perSubscriberBuffer + 256

	done := make(chan struct{})
	go func() {
		for i := 0; i < floodCount; i++ {
			m.Publish(entity.EventLog, info)
		}
		close(done)
	}()

	select {
	case <-done:
		// Every Publish returned even though the subscriber is stalled.
	case <-time.After(5 * time.Second):
		close(w.release) // unblock the handler so the flood goroutine can finish
		t.Fatal("Manager.Publish blocked on a stalled SSE subscriber; recording would stall (issue #34)")
	}

	close(w.release)
}
