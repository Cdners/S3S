package main

import (
	"context"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/transfer"
)

// StartUpload accepts an exact remote key, but only the native chooser may
// provide the local source. No file contents enter the WebView.
func (w *Workspace) StartUpload(ctx context.Context, c core.Connection, bucket, key, title string) response[string] {
	if w.store == nil || w.engine == nil {
		return failure[string](nil, w.unavailable())
	}
	if bucket == "" || key == "" {
		return failure[string](nil, core.InvalidTask)
	}
	if w.chooseSource == nil {
		return failure[string](nil, "error.dialog")
	}
	path, err := w.chooseSource(title)
	if err != nil {
		return failure[string](nil, "error.dialog")
	}
	if path == "" {
		return response[string]{Data: ""}
	}
	client, err := w.client(ctx, c)
	if err != nil {
		return failure[string](err, "error.cloudRequest")
	}
	enqueueCtx, finish, err := w.operation(ctx)
	if err != nil {
		return failure[string](err, "error.appClosing")
	}
	defer finish()
	task, err := w.engine.EnqueueUpload(enqueueCtx, c, bucket, key, path)
	if err != nil {
		return failure[string](err, "error.uploadSource")
	}
	if err = w.launchUpload(enqueueCtx, task.ID, c, client, false); err != nil {
		return failure[string](err, core.Storage)
	}
	return response[string]{Data: task.ID}
}

func (w *Workspace) launchUpload(ctx context.Context, id string, c core.Connection, dest transfer.Destination, cleanup bool) error {
	claim := func(request context.Context, id string) error {
		// RequestUploadCancel shares the worker mutex. A stale resume cannot
		// claim a task as a normal upload after cancellation was requested.
		task, err := w.store.Task(request, id)
		if err != nil {
			return err
		}
		if task.Direction != core.Upload || task.CancelRequested != cleanup {
			return core.NewError(core.InvalidTransition)
		}
		return w.store.ClaimUpload(request, id)
	}
	return w.startWorker(ctx, id, core.Upload, cleanup, claim, func(workerCtx context.Context) {
		w.engine.RunUploadClaimed(workerCtx, id, c, dest)
	})
}

func (w *Workspace) resumeUpload(ctx context.Context, id string, cleanup bool) response[bool] {
	t, err := w.store.Task(ctx, id)
	if err != nil {
		return failure[bool](err, core.Storage)
	}
	if !t.Resumable || t.Direction != core.Upload || t.CancelRequested != cleanup ||
		(t.State != core.Queued && t.State != core.Paused && t.State != core.Failed) {
		return failure[bool](nil, core.InvalidTransition)
	}
	u, err := w.store.Upload(ctx, id)
	if err != nil {
		return failure[bool](err, core.Storage)
	}
	var connection core.Connection
	var dest transfer.Destination
	// A local cancellation can be interrupted after the aborting phase is
	// recorded but before the terminal task state is committed.
	localCancel := cleanup && t.UploadID == "" && (u.Phase == "ready" || u.Phase == "aborting")
	if !localCancel {
		connection, err = w.store.Connection(ctx, t.ConnectionID)
		if err != nil {
			return failure[bool](err, core.Storage)
		}
		if connection.Provider != u.Provider || connection.Region != u.Region || connection.Endpoint != u.Endpoint {
			return failure[bool](nil, "error.connectionChanged")
		}
		dest, err = w.client(ctx, connection)
		if err != nil {
			return failure[bool](err, "error.cloudRequest")
		}
	}
	if err := w.launchUpload(ctx, id, connection, dest, cleanup); err != nil {
		return failure[bool](err, core.Storage)
	}
	return response[bool]{Data: true}
}

func (w *Workspace) ResumeUpload(ctx context.Context, id string) response[bool] {
	if w.store == nil || w.engine == nil {
		return failure[bool](nil, w.unavailable())
	}
	return w.resumeUpload(ctx, id, false)
}

func (w *Workspace) PauseUpload(ctx context.Context, id string) response[bool] {
	return w.pauseTransfer(ctx, id, core.Upload)
}

func (w *Workspace) CancelUpload(ctx context.Context, id string) response[bool] {
	if w.store == nil || w.engine == nil {
		return failure[bool](nil, w.unavailable())
	}
	w.mu.Lock()
	if w.closing {
		w.mu.Unlock()
		return failure[bool](nil, "error.appClosing")
	}
	job, active := w.active[id]
	if active && job.direction != core.Upload {
		w.mu.Unlock()
		return failure[bool](nil, core.InvalidTransition)
	}
	if active && job.cleanup {
		w.mu.Unlock()
		return response[bool]{Data: true}
	}
	// Commit the intent before telling the active SDK request to stop. On
	// restart the user can continue cleanup even if this process ended here.
	err := w.store.RequestUploadCancel(ctx, id)
	if err == nil && active {
		job.cancel()
	}
	w.mu.Unlock()
	if err != nil {
		// The worker may have finished between locating it and recording the
		// intent. Repeated cancellation of a terminal upload is a no-op.
		t, readErr := w.store.Task(ctx, id)
		if readErr == nil && t.Direction == core.Upload && (t.State == core.Canceled || t.State == core.Completed) {
			return response[bool]{Data: true}
		}
		return failure[bool](err, core.Storage)
	}
	if active {
		select {
		case <-job.done:
		case <-ctx.Done():
			return failure[bool](nil, "error.appClosing")
		}
	}
	w.mu.Lock()
	if other, busy := w.active[id]; busy && other.cleanup && !w.closing {
		w.mu.Unlock()
		return response[bool]{Data: true}
	}
	t, err := w.idleTaskLocked(ctx, id, core.Upload)
	w.mu.Unlock()
	if err != nil {
		return failure[bool](err, core.Storage)
	}
	if t.State == core.Canceled || t.State == core.Completed {
		// The provider may commit just before the cancellation becomes visible.
		// Return an acknowledgment; the UI reads the actual terminal state.
		return response[bool]{Data: true}
	}
	return w.resumeUpload(ctx, id, true)
}
