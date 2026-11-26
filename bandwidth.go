package main

import (
	"context"
	"net/http"

	"github.com/Cdners/S3S/internal/core"
)

func (w *Workspace) payloadTransport() http.RoundTripper {
	if w.limiter == nil {
		return w.storageTransport
	}
	return w.limiter.Transport(w.storageTransport)
}

func (w *Workspace) GetTransferOptions(ctx context.Context) response[core.TransferOptions] {
	if w.store == nil {
		return failure[core.TransferOptions](nil, w.unavailable())
	}
	o, err := w.store.TransferOptions(ctx)
	if err != nil {
		return failure[core.TransferOptions](err, "error.storageRead")
	}
	return response[core.TransferOptions]{Data: o}
}
func (w *Workspace) SaveTransferOptions(ctx context.Context, options core.TransferOptions) response[core.TransferOptions] {
	if w.store == nil || w.limiter == nil {
		return failure[core.TransferOptions](nil, w.unavailable())
	}
	// Preserve commit order when simultaneous windows submit settings.
	w.optionsMu.Lock()
	defer w.optionsMu.Unlock()
	o, err := w.store.SaveTransferOptions(ctx, options)
	if err != nil {
		return failure[core.TransferOptions](err, "error.storageWrite")
	}
	w.limiter.Set(o)
	return response[core.TransferOptions]{Data: o}
}
