//go:build s3integration

package integration_test

import (
	"context"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/ossclient"
	"github.com/Cdners/S3S/internal/tosclient"
	"github.com/Cdners/S3S/internal/transfer"
)

type regionalReader interface {
	transfer.Source
	ListBuckets(context.Context, string) (core.BucketPage, error)
	ListObjects(context.Context, string, string, string) (core.ObjectPage, error)
}

func TestRegionalSignedReads(t *testing.T) {
	for _, tc := range []struct {
		provider             core.Provider
		region, host, bucket string
	}{
		{core.TOS, "cn-beijing", "tos-cn-beijing.volces.com", "design-assets"},
		{core.OSS, "cn-hangzhou", "oss-cn-hangzhou.aliyuncs.com", "project-archive"},
	} {
		t.Run(string(tc.provider), func(t *testing.T) {
			requireLoopback(t, tc.host)
			requireLoopback(t, tc.bucket+"."+tc.host)
			var input struct{ AccessKeyID, SecretAccessKey string }
			readJSON(t, "/fixture/credentials.json", &input)
			newReader := func(secret string) regionalReader {
				t.Helper()
				config := core.Connection{ID: string(tc.provider), Name: "Offline fixture", Provider: tc.provider, Region: tc.region, Endpoint: "https://" + tc.host}
				keys := core.CloudCredentials{AccessKeyID: input.AccessKeyID, SecretAccessKey: secret}
				var reader regionalReader
				var err error
				switch tc.provider {
				case core.TOS:
					reader, err = tosclient.New(config, keys, nil)
				case core.OSS:
					reader, err = ossclient.New(config, keys, nil)
				}
				if err != nil {
					t.Fatal(err)
				}
				return reader
			}
			ctx := t.Context()
			reader := newReader(input.SecretAccessKey)
			buckets, err := reader.ListBuckets(ctx, "")
			if tc.provider == core.OSS {
				if err != nil || !slices.Contains(buckets.Buckets, tc.bucket) {
					t.Fatalf("list account: %v", err)
				}
			} else {
				requireServiceCode(t, err, "AccessDenied", tc.provider)
			}
			const key = "项目 +%2F.txt"
			page, err := reader.ListObjects(ctx, tc.bucket, "", "")
			if err != nil || len(page.Objects) != 1 || page.Objects[0].Key != key {
				t.Fatalf("list scoped objects: %v", err)
			}
			id, err := reader.Head(ctx, tc.bucket, key)
			if err != nil || id.Size != page.Objects[0].Size || id.ETag != page.Objects[0].ETag {
				t.Fatalf("head object: %v", err)
			}
			part, err := reader.ReadRange(ctx, tc.bucket, key, id, 7, 8)
			if err != nil {
				t.Fatal(err)
			}
			defer part.Close()
			data, err := io.ReadAll(part)
			if err != nil || string(data) != "metadata" {
				t.Fatalf("conditional range: %v", err)
			}
			// Same access key ID with a different secret must fail the fixture's
			// independent request-signature verification for both providers.
			_, err = newReader(input.SecretAccessKey+"x").ListObjects(ctx, tc.bucket, "", "")
			requireServiceCode(t, err, "SignatureDoesNotMatch", tc.provider)
		})
	}
}

func requireServiceCode(t *testing.T, err error, code string, provider core.Provider) {
	t.Helper()
	var detail *core.Error
	if !errors.As(err, &detail) || detail.ProviderCode != code || detail.RequestID != "isolated-"+string(provider)+"-request" {
		t.Fatalf("expected %s and fixture request ID, got %v", code, err)
	}
}
