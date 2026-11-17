package daemon

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// Serve serves on ln until ctx is done, then stops accepting connections and
// lets requests already running finish, for at most grace. It returns only
// after they have.
//
// That last part is the point. http.Server.Serve returns the moment Shutdown
// is called, while Shutdown is still waiting for requests to drain; a process
// that exits when Serve returns would cut off a multi-step write (an issue and
// then its edges) partway, which is the very thing handling SIGTERM is for.
func Serve(ctx context.Context, srv *http.Server, ln net.Listener, grace time.Duration) error {
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	select {
	case err := <-served:
		// It stopped on its own: the listener failed.
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()
	err := srv.Shutdown(drain)
	serveErr := <-served
	if err != nil {
		// Requests still running when the grace period ran out are cut off.
		_ = srv.Close()
		return err
	}
	// After a Shutdown Serve reports ErrServerClosed; anything else is a
	// failure that raced with it and should not be swallowed.
	if !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}
