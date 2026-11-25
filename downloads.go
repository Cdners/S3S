package main

import (
	"context"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/transfer"
)

func (w *Workspace) launch(ctx context.Context, id string, c core.Connection, source transfer.Source) error {
	return w.startWorker(ctx, id, core.Download, false, w.store.ClaimDownload, func(workerCtx context.Context) {
		w.engine.RunClaimed(workerCtx, id, c, source)
	})
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
	enqueueCtx, finish, err := w.operation(ctx)
	if err != nil {
		return failure[string](err, "error.appClosing")
	}
	defer finish()
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
	if !t.Resumable || t.Direction != core.Download || (t.State != core.Queued && t.State != core.Paused && t.State != core.Failed) {
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
	return w.pauseTransfer(ctx, id, core.Download)
}
