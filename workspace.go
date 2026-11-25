package main

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"sync"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/store"
	"github.com/Cdners/S3S/internal/transfer"
	"github.com/gofrs/flock"
)

// Workspace exposes metadata and write-only credential provisioning. Object
// bytes and keyring reads always stay in Go.
type Workspace struct {
	openIssue         core.ErrorCode
	life              context.Context
	stop              context.CancelFunc
	store             *store.Store
	lock              *flock.Flock
	engine            *transfer.Engine
	storageTransport  http.RoundTripper
	chooseSource      func(string) (string, error)
	chooseDestination func(string) (string, error)

	// Serialize a profile edit with checking that a credential write still targets it.
	profilesMu sync.Mutex
	mu         sync.Mutex
	closing    bool
	active     map[string]worker
	wg         sync.WaitGroup
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

func (w *Workspace) unavailable() core.ErrorCode {
	if w.openIssue != "" {
		return w.openIssue
	}
	return "error.storageUnavailable"
}

func (w *Workspace) Connections(ctx context.Context) response[[]core.Connection] {
	if w.store == nil {
		return failure[[]core.Connection](nil, w.unavailable())
	}
	connections, err := w.store.Connections(ctx)
	if err != nil {
		return failure[[]core.Connection](err, "error.storageRead")
	}
	return response[[]core.Connection]{Data: connections}
}

func (w *Workspace) SaveConnection(ctx context.Context, connection core.Connection) response[*core.Connection] {
	if w.store == nil {
		return failure[*core.Connection](nil, w.unavailable())
	}
	if connection.ID == "" {
		connection.ID = rand.Text()
	}
	w.profilesMu.Lock()
	defer w.profilesMu.Unlock()
	if err := w.store.SaveConnection(ctx, connection); err != nil {
		return failure[*core.Connection](err, "error.storageWrite")
	}
	return response[*core.Connection]{Data: &connection}
}

func (w *Workspace) Tasks(ctx context.Context) response[[]core.Task] {
	if w.store == nil {
		return failure[[]core.Task](nil, w.unavailable())
	}
	tasks, err := w.store.Tasks(ctx)
	if err != nil {
		return failure[[]core.Task](err, "error.storageRead")
	}
	// Only the engine reopens an upload source. Its absolute native path is
	// unnecessary for task progress or control in the renderer.
	for i := range tasks {
		if tasks[i].Direction == core.Upload {
			tasks[i].LocalPath = ""
		}
	}
	return response[[]core.Task]{Data: tasks}
}
