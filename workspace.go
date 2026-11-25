package main

import (
	"context"
	"crypto/rand"
	"errors"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/store"
)

// Workspace exposes metadata only. Object bytes and credentials never pass
// through this desktop bridge.
type Workspace struct {
	store *store.Store
}

type response[T any] struct {
	Data  T           `json:"data"`
	Error *core.Error `json:"error"`
}

// Only structured domain errors may cross IPC. Other errors (including raw
// database/driver errors) become a stable localized fallback.
func failure[T any](err error, fallback core.ErrorCode) response[T] {
	var detail *core.Error
	if !errors.As(err, &detail) {
		detail = core.NewError(fallback)
	}
	return response[T]{Error: detail}
}

func (w *Workspace) Connections(ctx context.Context) response[[]core.Connection] {
	if w.store == nil {
		return failure[[]core.Connection](nil, "error.storageUnavailable")
	}
	connections, err := w.store.Connections(ctx)
	if err != nil {
		return failure[[]core.Connection](err, "error.storageRead")
	}
	return response[[]core.Connection]{Data: connections}
}

func (w *Workspace) SaveConnection(ctx context.Context, connection core.Connection) response[*core.Connection] {
	if w.store == nil {
		return failure[*core.Connection](nil, "error.storageUnavailable")
	}
	if connection.ID == "" {
		connection.ID = rand.Text()
	}
	if err := w.store.SaveConnection(ctx, connection); err != nil {
		return failure[*core.Connection](err, "error.storageWrite")
	}
	return response[*core.Connection]{Data: &connection}
}

func (w *Workspace) Tasks(ctx context.Context) response[[]core.Task] {
	if w.store == nil {
		return failure[[]core.Task](nil, "error.storageUnavailable")
	}
	tasks, err := w.store.Tasks(ctx)
	if err != nil {
		return failure[[]core.Task](err, "error.storageRead")
	}
	return response[[]core.Task]{Data: tasks}
}
