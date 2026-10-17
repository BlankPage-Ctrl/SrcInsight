package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"srcinsight/internal/watch"
	"srcinsight/services/methods"
	"srcinsight/transport/stdio"
)

// EnsureIndexed converges the vault on startup; the bool reports a fresh index.
func (p *Pipeline) EnsureIndexed(ctx context.Context) (SyncResult, bool, error) {
	tracked, err := p.vault.FetchTracked(ctx)
	if err != nil {
		return SyncResult{}, false, err
	}
	if len(tracked) == 0 {
		res, err := p.FullScan(ctx, true)
		return res, true, err
	}
	res, err := p.FullScan(ctx, false)
	return res, false, err
}

// Serve indexes, then serves JSON-RPC on stdin/stdout until shutdown or watcher death.
// A held write mutex answers busy so reads stay answerable.
func (p *Pipeline) Serve(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	if _, _, err := p.EnsureIndexed(ctx); err != nil {
		return err
	}

	if reason := watch.WatchDisabledReason(p.rootPath); reason != "" {
		return die(stdout, fmt.Errorf("watcher: %s", reason))
	}
	w, err := p.NewWatcher()
	if err != nil {
		return die(stdout, fmt.Errorf("watcher: %w", err))
	}
	defer w.Stop()

	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	watchErr := make(chan error, 1)
	go func() {
		if err := w.Start(serveCtx); err != nil && ctx.Err() == nil {
			select {
			case watchErr <- err:
			default:
			}
			cancel()
		}
	}()

	// Expose the running watcher so `status` can report syncing/pending.
	p.setWatcher(w)
	defer p.setWatcher(nil)

	srv := stdio.New()

	methods.MustRegister(srv, "search", methods.New(p.Vault()).HandleSearch)
	methods.MustRegister(srv, "graph", methods.New(p.Vault()).HandleGraph)
	methods.MustRegister(srv, "refs", methods.New(p.Vault(), methods.WithLive(p)).HandleRefs)
	methods.MustRegister(srv, "trace", methods.New(p.Vault()).HandleTrace)
	methods.MustRegister(srv, stdio.MethodSync, p.HandleSync)
	methods.MustRegister(srv, stdio.MethodIndex, p.HandleIndex)
	methods.MustRegister(srv, stdio.MethodStatus, p.HandleStatus)

	loopErr := srv.Loop(serveCtx, stdin, stdout)

	if loopErr != nil {
		return die(stdout, loopErr)
	}
	select {
	case err := <-watchErr:
		return die(stdout, fmt.Errorf("watcher: %w", err))
	default:
		return nil
	}
}

// die emits the terminal frame, then returns reason.
func die(stdout io.Writer, reason error) error {
	_ = json.NewEncoder(stdout).Encode(stdio.ServerError("serve: " + reason.Error()))
	return fmt.Errorf("serve: %w", reason)
}
