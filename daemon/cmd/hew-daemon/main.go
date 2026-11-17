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
	"syscall"
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
	initLocal := flag.Bool("init-local", false, "start a local tracker in --dir if it has none (refused where issues live on GitHub)")
	flag.Parse()

	if err := daemon.ValidateOptions(*addr, *repo, *initLocal); err != nil {
		return err
	}
	// SIGTERM is what kill, docker stop and systemd send. Handling it as well
	// as Ctrl-C lets an in-flight multi-step write (an issue and then its
	// edges) finish before the process exits.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *initLocal {
		notices, err := daemon.EnsureLocalTracker(ctx, *dir)
		if err != nil {
			return err
		}
		for _, n := range notices {
			fmt.Fprintln(os.Stderr, "hew-daemon:", n)
		}
	}

	cfg := tracker.Config{Worktree: *dir, Repo: *repo, Notice: os.Stderr}
	// Detect once and pin the backend for the life of the process, as the
	// library's long-lived-process guidance asks.
	target, err := daemon.ResolveTarget(*dir, *repo)
	if err != nil {
		return err
	}
	cfg.Backend = target.Backend
	tr, err := tracker.Open(ctx, cfg)
	if err != nil {
		return err
	}

	// A nil *pr.Client stored in the interface would not be a nil interface,
	// so the routes' "no pull requests" check would pass and then panic.
	var prs daemon.PullRequests
	if tr.Backend() == tracker.BackendGitHub {
		prc, err := pr.Open(ctx, pr.Config{Worktree: *dir, Backend: tracker.BackendGitHub, Repo: *repo})
		var none *pr.NoPullRequestsError
		switch {
		case err == nil:
			prs = prc
		case !errors.As(err, &none):
			return err
		}
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           daemon.New(tr, prs, target).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Printf("hew-daemon: %s tracker %s — http://%s\n", tr.Backend(), tr.Repo(), ln.Addr())
	return daemon.Serve(ctx, srv, ln, 10*time.Second)
}
