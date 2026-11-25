package main

import (
	"context"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/s3client"
	"github.com/Cdners/S3S/internal/vault"
)

// The renderer supplies the profile it is displaying; SQLite is authoritative.
// A stale form cannot bind a new secret to an endpoint edited in another view.
func (w *Workspace) checkProfile(ctx context.Context, expected core.Connection) (core.Connection, error) {
	if w.store == nil {
		return core.Connection{}, core.NewError(w.unavailable())
	}
	current, err := w.store.Connection(ctx, expected.ID)
	if err != nil {
		return core.Connection{}, err
	}
	if vault.ScopeOf(current) != vault.ScopeOf(expected) {
		return core.Connection{}, core.NewError("error.connectionChanged")
	}
	if current.Provider != core.AWS && current.Provider != core.R2 {
		return core.Connection{}, core.NewError("error.cloudProvider")
	}
	return current, nil
}

func (w *Workspace) SetCredentials(ctx context.Context, expected core.Connection, input vault.Credentials) response[bool] {
	w.profilesMu.Lock()
	defer w.profilesMu.Unlock()
	c, err := w.checkProfile(ctx, expected)
	if err == nil {
		// Validate the configured origin and region before saving keys. Creating
		// an SDK client performs no network request or credential discovery.
		_, err = s3client.NewWithTransport(c, sdkCredentials(input), w.storageTransport)
	}
	if err == nil {
		err = (vault.Vault{}).Save(c, input)
	}
	if err != nil {
		return failure[bool](err, "error.keyringUnavailable")
	}
	return response[bool]{Data: true}
}

func (w *Workspace) ForgetCredentials(ctx context.Context, expected core.Connection) response[bool] {
	w.profilesMu.Lock()
	defer w.profilesMu.Unlock()
	// Clearing obsolete credentials must also work after a profile is retargeted
	// to a provider for which the transfer adapter is not yet enabled.
	if w.store == nil {
		return failure[bool](nil, w.unavailable())
	}
	c, err := w.store.Connection(ctx, expected.ID)
	if err == nil && vault.ScopeOf(c) != vault.ScopeOf(expected) {
		err = core.NewError("error.connectionChanged")
	}
	if err == nil {
		err = (vault.Vault{}).Forget(c)
	}
	if err != nil {
		return failure[bool](err, "error.keyringUnavailable")
	}
	return response[bool]{Data: true}
}

func sdkCredentials(input vault.Credentials) s3client.Credentials {
	return s3client.Credentials{AccessKeyID: input.AccessKeyID, SecretAccessKey: input.SecretAccessKey, SessionToken: input.SessionToken}
}

func (w *Workspace) client(ctx context.Context, expected core.Connection) (*s3client.Client, error) {
	c, err := w.checkProfile(ctx, expected)
	if err != nil {
		return nil, err
	}
	credentials, err := (vault.Vault{}).Load(c)
	if err != nil {
		return nil, err
	}
	return s3client.NewWithTransport(c, sdkCredentials(credentials), w.storageTransport)
}

func (w *Workspace) ListBuckets(ctx context.Context, c core.Connection, token string) response[*s3client.BucketPage] {
	client, err := w.client(ctx, c)
	if err != nil {
		return failure[*s3client.BucketPage](err, "error.cloudRequest")
	}
	page, err := client.ListBuckets(ctx, token)
	if err != nil {
		return failure[*s3client.BucketPage](err, "error.cloudRequest")
	}
	return response[*s3client.BucketPage]{Data: &page}
}

func (w *Workspace) ListObjects(ctx context.Context, c core.Connection, bucket, prefix, token string) response[*s3client.ObjectPage] {
	client, err := w.client(ctx, c)
	if err != nil {
		return failure[*s3client.ObjectPage](err, "error.cloudRequest")
	}
	page, err := client.ListObjects(ctx, bucket, prefix, token)
	if err != nil {
		return failure[*s3client.ObjectPage](err, "error.cloudRequest")
	}
	return response[*s3client.ObjectPage]{Data: &page}
}
