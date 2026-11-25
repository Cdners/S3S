package main

import (
	"context"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/transfer"
)

type worker struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (w *Workspace) close() {
	w.mu.Lock()
	w.closing = true
	if w.stop != nil {
		w.stop()
	}
	w.mu.Unlock()
	w.wg.Wait()
	if w.store != nil {
		w.store.Close()
	}
	if w.lock != nil {
		w.lock.Unlock()
	}
}

func (w *Workspace) launch(request context.Context, id string, c core.Connection, source transfer.Source) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closing {
		return core.NewError("error.appClosing")
	}
	if _, ok := w.active[id]; ok {
		return core.NewError("error.downloadBusy")
	}
	if err := w.store.ClaimDownload(request, id); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(w.life)
	job := worker{cancel: cancel, done: make(chan struct{})}
	w.active[id] = job
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer cancel()
		// The engine persists its terminal state and safe errors before returning.
		w.engine.RunClaimed(ctx, id, c, source)
		w.mu.Lock()
		delete(w.active, id)
		close(job.done)
		w.mu.Unlock()
	}()
	return nil
}

func (w *Workspace) StartDownload(ctx context.Context, c core.Connection, bucket, key, title string) response[string] {
	if w.store == nil || w.engine == nil {
		return failure[string](nil, w.unavailable())
	}
	if w.chooseDestination == nil {
		return failure[string](nil, "error.dialog")
	}
	// The object key is opaque and never passed to the file dialog or filepath.
	path, err := w.chooseDestination(title)
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
	// HEAD and manifest creation can run while the window is closing. Cancel and
	// join this operation before releasing the database lock to another process.
	w.mu.Lock()
	if w.closing {
		w.mu.Unlock()
		return failure[string](nil, "error.appClosing")
	}
	w.wg.Add(1)
	w.mu.Unlock()
	defer w.wg.Done()
	enqueueCtx, cancel := context.WithCancel(ctx)
	stopCancel := context.AfterFunc(w.life, cancel)
	defer func() { stopCancel(); cancel() }()
	task, err := w.engine.Enqueue(enqueueCtx, c, client, bucket, key, path)
	if err != nil {
		return failure[string](err, "error.downloadFile")
	}
	if err := w.launch(enqueueCtx, task.ID, c, client); err != nil {
		return failure[string](err, "error.downloadFile")
	}
	return response[string]{Data: task.ID}
}

func (w *Workspace) ResumeDownload(ctx context.Context, id string) response[bool] {
	if w.store == nil || w.engine == nil {
		return failure[bool](nil, w.unavailable())
	}
	t, err := w.store.Task(ctx, id)
	if err != nil {
		return failure[bool](err, core.Storage)
	}
	if !t.Resumable || (t.State != core.Queued && t.State != core.Paused && t.State != core.Failed) {
		return failure[bool](nil, core.InvalidTransition)
	}
	c, err := w.store.Connection(ctx, t.ConnectionID)
	if err != nil {
		return failure[bool](err, core.Storage)
	}
	d, err := w.store.Download(ctx, id)
	if err != nil {
		return failure[bool](err, core.Storage)
	}
	if c.Provider != d.Provider || c.Region != d.Region || c.Endpoint != d.Endpoint {
		return failure[bool](nil, "error.connectionChanged")
	}
	client, err := w.client(ctx, c)
	if err != nil {
		return failure[bool](err, "error.cloudRequest")
	}
	if err := w.launch(ctx, id, c, client); err != nil {
		return failure[bool](err, "error.downloadFile")
	}
	return response[bool]{Data: true}
}

func (w *Workspace) PauseDownload(ctx context.Context, id string) response[bool] {
	if w.store == nil {
		return failure[bool](nil, w.unavailable())
	}
	w.mu.Lock()
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
	t, err := w.store.Task(ctx, id)
	if err != nil {
		return failure[bool](err, core.Storage)
	}
	if !t.Resumable {
		return failure[bool](nil, core.InvalidTransition)
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
