package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/ossclient"
	"github.com/Cdners/S3S/internal/s3client"
	"github.com/Cdners/S3S/internal/tosclient"
	"github.com/Cdners/S3S/internal/transfer"
	"github.com/Cdners/S3S/internal/vault"
)

// The browser and transfer engine consume the same client for a connection.
// Upload is an additional, separately gated capability.
type readClient interface {
	transfer.Source
	ListBuckets(context.Context, string) (core.BucketPage, error)
	ListObjects(context.Context, string, string, string) (core.ObjectPage, error)
}

func newCloudClient(c core.Connection, input vault.Credentials, transport http.RoundTripper) (readClient, error) {
	creds := core.CloudCredentials{AccessKeyID: input.AccessKeyID, SecretAccessKey: input.SecretAccessKey, SessionToken: input.SessionToken}
	switch c.Provider {
	case core.AWS, core.R2, core.S3Compatible:
		return s3client.NewWithTransport(c, creds, transport)
	case core.OSS:
		return ossclient.New(c, creds, transport)
	case core.TOS:
		return tosclient.New(c, creds, transport)
	default:
		return nil, core.NewError("error.cloudProvider")
	}
}

func (w *Workspace) Capabilities() response[map[core.Provider]core.Capabilities] {
	caps := make(map[core.Provider]core.Capabilities, 5)
	for _, p := range []core.Provider{core.AWS, core.R2, core.OSS, core.TOS, core.S3Compatible} {
		caps[p] = core.CapabilitiesFor(p)
	}
	return response[map[core.Provider]core.Capabilities]{Data: caps}
}

func (w *Workspace) client(ctx context.Context, expected core.Connection) (readClient, error) {
	c, err := w.currentProfile(ctx, expected)
	if err != nil {
		return nil, err
	}
	credentials, err := (vault.Vault{}).Load(c)
	if err != nil {
		return nil, err
	}
	return newCloudClient(c, credentials, w.payloadTransport())
}

func (w *Workspace) destination(ctx context.Context, c core.Connection) (transfer.Destination, error) {
	if !core.CapabilitiesFor(c.Provider).Upload {
		return nil, core.NewError("error.cloudProvider")
	}
	client, err := w.client(ctx, c)
	if err != nil {
		return nil, err
	}
	dest, ok := client.(transfer.Destination)
	if !ok {
		return nil, core.NewError("error.cloudProvider")
	}
	return dest, nil
}

func (w *Workspace) ListBuckets(ctx context.Context, c core.Connection, token string) response[*core.BucketPage] {
	if !core.CapabilitiesFor(c.Provider).ListBuckets {
		return failure[*core.BucketPage](nil, "error.cloudProvider")
	}
	client, err := w.client(ctx, c)
	if err != nil {
		return failure[*core.BucketPage](err, "error.cloudRequest")
	}
	page, err := client.ListBuckets(ctx, token)
	if err != nil {
		return failure[*core.BucketPage](err, "error.cloudRequest")
	}
	return response[*core.BucketPage]{Data: &page}
}

func (w *Workspace) ListObjects(ctx context.Context, c core.Connection, bucket, prefix, token string) response[*core.ObjectPage] {
	if !core.CapabilitiesFor(c.Provider).ListObjects {
		return failure[*core.ObjectPage](nil, "error.cloudProvider")
	}
	client, err := w.client(ctx, c)
	if err != nil {
		return failure[*core.ObjectPage](err, "error.cloudRequest")
	}
	page, err := client.ListObjects(ctx, bucket, prefix, token)
	if err != nil {
		return failure[*core.ObjectPage](err, "error.cloudRequest")
	}
	return response[*core.ObjectPage]{Data: &page}
}

// ObjectMetadata performs a fresh signed HEAD. The object key remains opaque.
func (w *Workspace) ObjectMetadata(ctx context.Context, c core.Connection, bucket, key string) response[*core.ObjectIdentity] {
	if !core.CapabilitiesFor(c.Provider).Metadata {
		return failure[*core.ObjectIdentity](nil, "error.cloudProvider")
	}
	client, err := w.client(ctx, c)
	if err != nil {
		return failure[*core.ObjectIdentity](err, "error.cloudRequest")
	}
	meta, err := client.Head(ctx, bucket, key)
	if err != nil {
		return failure[*core.ObjectIdentity](err, "error.cloudRequest")
	}
	return response[*core.ObjectIdentity]{Data: &meta}
}

type probeResult struct {
	Operation string `json:"operation"`
	LatencyMS int64  `json:"latencyMs"`
}

// TestConnection issues one logical, authenticated read operation. A known
// bucket bypasses account-wide bucket listing. SDK retries share the deadline.
func (w *Workspace) TestConnection(ctx context.Context, c core.Connection, bucket string) response[*probeResult] {
	caps := core.CapabilitiesFor(c.Provider)
	if (bucket == "" && !caps.ListBuckets) || (bucket != "" && !caps.ListObjects) {
		return failure[*probeResult](nil, "error.cloudProvider")
	}
	client, err := w.client(ctx, c)
	if err != nil {
		return failure[*probeResult](err, "error.cloudRequest")
	}
	read, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	started := time.Now()
	report := probeResult{Operation: "listBuckets"}
	if bucket == "" {
		_, err = client.ListBuckets(read, "")
	} else {
		report.Operation = "listObjects"
		_, err = client.ListObjects(read, bucket, "", "")
	}
	report.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		out := failure[*probeResult](err, "error.cloudRequest")
		if errors.Is(read.Err(), context.DeadlineExceeded) {
			detail := *out.Error
			detail.Code = "error.cloudTimeout"
			detail.Retryable = true
			out.Error = &detail
		}
		out.Data = &report
		return out
	}
	return response[*probeResult]{Data: &report}
}
