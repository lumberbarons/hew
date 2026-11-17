package daemon_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/lumberbarons/hew/daemon"
)

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

// TestServeFinishesARunningRequestBeforeItReturns is the hazard behind
// handling SIGTERM at all: http.Server.Serve returns the instant Shutdown is
// called, so a process that exits on Serve's return cuts a multi-step write
// off partway. daemon.Serve must not return until the request is done.
func TestServeFinishesARunningRequestBeforeItReturns(t *testing.T) {
	ln := listen(t)
	started, release := make(chan struct{}), make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "finished")
	})}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	served := make(chan error, 1)
	go func() { served <- daemon.Serve(ctx, srv, ln, 5*time.Second) }()

	body := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String())
		if err != nil {
			body <- "error: " + err.Error()
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		body <- string(b)
	}()

	<-started
	stop() // the signal arrives while the request is running
	select {
	case err := <-served:
		t.Fatalf("Serve returned (%v) while a request was still running", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	if got := <-body; got != "finished" {
		t.Errorf("the client got %q, want the response of the request that was running", got)
	}
	if err := <-served; err != nil {
		t.Errorf("Serve = %v after a clean drain, want nil", err)
	}
}

func TestServeGivesUpOnARequestThatOutlastsTheGracePeriod(t *testing.T) {
	ln := listen(t)
	stuck := make(chan struct{})
	started := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-stuck
	})}
	defer close(stuck)
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- daemon.Serve(ctx, srv, ln, 100*time.Millisecond) }()

	go func() { _, _ = http.Get("http://" + ln.Addr().String()) }()
	<-started
	stop()
	select {
	case err := <-served:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Serve = %v, want the grace period's deadline", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after the grace period")
	}
}

func TestServeStopsCleanlyWhenNothingIsRunning(t *testing.T) {
	ln := listen(t)
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- daemon.Serve(ctx, &http.Server{}, ln, time.Second) }()
	stop()
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("Serve = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return")
	}
}

func TestServeReportsAListenerThatFails(t *testing.T) {
	ln := listen(t)
	_ = ln.Close()
	if err := daemon.Serve(context.Background(), &http.Server{}, ln, time.Second); err == nil {
		t.Error("Serve on a closed listener = nil, want the failure")
	}
}
