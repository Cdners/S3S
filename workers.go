package main

import (
	"context"

	"github.com/Cdners/S3S/internal/core"
)

type worker struct {
	cancel    context.CancelFunc
	done      chan struct{}
	direction core.Direction
	cleanup   bool
}

func (w *Workspace) close() {
	w.mu.Lock()
	w.closing = true
	if w.stop != nil {
		w.stop()
	}
	w.mu.Unlock()
	w.wg.Wait()
	// A pending OS keychain prompt may still be committing an explicit
	// credential write. Finish it before another process can edit this profile.
	w.profilesMu.Lock()
	defer w.profilesMu.Unlock()
	if w.store != nil {
		w.store.Close()
	}
	if w.lock != nil {
		w.lock.Unlock()
	}
}

// A foreground operation (for example hashing an upload) belongs to both the
// initiating RPC and this workspace. Complete it before releasing the DB lock.
func (w *Workspace) operation(request context.Context) (context.Context, func(), error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closing {
		return nil, nil, core.NewError("error.appClosing")
	}
	ctx, cancel := context.WithCancel(request)
	stop := context.AfterFunc(w.life, cancel)
	w.wg.Add(1)
	return ctx, func() { stop(); cancel(); w.wg.Done() }, nil
}

// Claim durably before returning a task ID. Once claimed, a transfer follows
// the workspace lifetime even if the view or language changes.
func (w *Workspace) startWorker(request context.Context, id string, direction core.Direction, cleanup bool,
	claim func(context.Context, string) error, run func(context.Context)) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closing {
		return core.NewError("error.appClosing")
	}
	if _, busy := w.active[id]; busy {
		return core.NewError("error.downloadBusy")
	}
	if err := claim(request, id); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(w.life)
	job := worker{direction: direction, cleanup: cleanup, cancel: cancel, done: make(chan struct{})}
	w.active[id] = job
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer cancel()
		run(ctx)
		w.mu.Lock()
		delete(w.active, id)
		close(job.done)
		w.mu.Unlock()
	}()
	return nil
}

func (w *Workspace) pauseTransfer(ctx context.Context, id string, dir core.Direction) response[bool] {
	if w.store == nil {
		return failure[bool](nil, w.unavailable())
	}
	w.mu.Lock()
	t, err := w.store.Task(ctx, id)
	if err != nil {
		w.mu.Unlock()
		return failure[bool](err, core.Storage)
	}
	if t.Direction != dir || !t.Resumable || t.CancelRequested {
		w.mu.Unlock()
		return failure[bool](nil, core.InvalidTransition)
	}
	job, active := w.active[id]
	if active {
		job.cancel()
	}
	w.mu.Unlock()
	if active {
		select {
		case <-job.done:
		case <-ctx.Done():
			return failure[bool](nil, "error.appClosing")
		}
	}
	t, err = w.store.Task(ctx, id)
	if err != nil {
		return failure[bool](err, core.Storage)
	}
	switch t.State {
	case core.Paused:
	case core.Queued:
		if err := w.store.Transition(ctx, id, core.Queued, core.Paused); err != nil {
			return failure[bool](err, core.Storage)
		}
	default:
		return failure[bool](nil, core.InvalidTransition)
	}
	return response[bool]{Data: true}
}
