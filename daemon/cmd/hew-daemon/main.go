// Command hew-daemon serves a hew tracker over REST plus a small web UI.
// It is a scratch proof of the public library: no auth, loopback only.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/lumberbarons/hew/daemon"
	"github.com/lumberbarons/hew/pr"
	"github.com/lumberbarons/hew/tracker"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "hew-daemon:", err)
		os.Exit(1)
	}
}

func run() error {
	dir := flag.String("dir", ".", "worktree to serve")
	addr := flag.String("addr", "127.0.0.1:7777", "loopback address to listen on")
	repo := flag.String("repo", "", "force the GitHub backend for owner/name")
	flag.Parse()

	if err := daemon.ValidateAddr(*addr); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cfg := tracker.Config{Worktree: *dir, Repo: *repo}
	if *repo != "" {
		cfg.Backend = tracker.BackendGitHub
	}
	// Detect once and pin the backend for the life of the process, as the
	// library's long-lived-process guidance asks.
	target, err := tracker.Detect(*dir)
	if err != nil {
		return err
	}
	if cfg.Backend == "" {
		cfg.Backend = target.Backend
	}
	tr, err := tracker.Open(ctx, cfg)
	if err != nil {
		return err
	}

	var prc *pr.Client
	if tr.Backend() == tracker.BackendGitHub {
		if prc, err = pr.Open(ctx, pr.Config{Worktree: *dir, Backend: tracker.BackendGitHub, Repo: *repo}); err != nil {
			var none *pr.NoPullRequestsError
			if !errors.As(err, &none) {
				return err
			}
			prc = nil
		}
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           daemon.New(tr, prc, target).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	fmt.Printf("hew-daemon: %s tracker %s — http://%s\n", tr.Backend(), tr.Repo(), ln.Addr())
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
