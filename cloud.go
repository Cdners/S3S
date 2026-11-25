package main

import (
	"context"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/s3client"
	"github.com/Cdners/S3S/internal/vault"
)

func sdkCredentials(input vault.Credentials) s3client.Credentials {
	return s3client.Credentials{AccessKeyID: input.AccessKeyID, SecretAccessKey: input.SecretAccessKey, SessionToken: input.SessionToken}
}

func (w *Workspace) client(ctx context.Context, expected core.Connection) (*s3client.Client, error) {
	c, err := w.currentProfile(ctx, expected)
	if err != nil {
		return nil, err
	}
	if c.Provider != core.AWS && c.Provider != core.R2 {
		return nil, core.NewError("error.cloudProvider")
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
