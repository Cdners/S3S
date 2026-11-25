//go:build livecloud

// Package live checks an explicitly provisioned AWS S3 or Cloudflare R2 fixture.
package live

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"testing"
	"time"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/s3client"
)

func required(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("set %s for the chosen fixture", name)
	}
	return value
}

func TestReadFixture(t *testing.T) {
	provider := core.Provider(required(t, "S3S_TEST_PROVIDER"))
	if provider != core.AWS && provider != core.R2 {
		t.Fatal("S3S_TEST_PROVIDER must be aws or r2")
	}
	conn := core.Connection{
		ID: "live-read", Name: "live-read", Provider: provider,
		Region: os.Getenv("S3S_TEST_REGION"), Endpoint: os.Getenv("S3S_TEST_ENDPOINT"),
	}
	bucket := required(t, "S3S_TEST_BUCKET")
	key := required(t, "S3S_TEST_KEY")
	wantDigest, err := hex.DecodeString(required(t, "S3S_TEST_SHA256"))
	if err != nil || len(wantDigest) != sha256.Size {
		t.Fatal("S3S_TEST_SHA256 must contain the fixture's 64-digit hex digest")
	}
	client, err := s3client.New(conn, s3client.Credentials{
		AccessKeyID:     required(t, "S3S_TEST_ACCESS_KEY_ID"),
		SecretAccessKey: required(t, "S3S_TEST_SECRET_ACCESS_KEY"),
		SessionToken:    os.Getenv("S3S_TEST_SESSION_TOKEN"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()

	// An exact prefix permits testing a scoped bucket credential without listing the entire account.
	page, err := client.ListObjects(ctx, bucket, key, "")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, object := range page.Objects {
		if object.Key == key {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("fixture missing from its prefix listing")
	}

	identity, err := client.Head(ctx, bucket, key)
	if err != nil {
		t.Fatal(err)
	}
	const maxFixtureBytes = 1024 * 1024
	if identity.Size <= 0 || identity.Size > maxFixtureBytes {
		t.Fatal("use a fixture between 1 byte and 1 MiB")
	}
	body, err := client.ReadRange(ctx, bucket, key, identity, 0, identity.Size)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(body, identity.Size+1))
	if err != nil {
		t.Fatal("fixture read failed")
	}
	if n != identity.Size || !bytes.Equal(h.Sum(nil), wantDigest) {
		t.Fatal("fixture length or SHA-256 mismatch")
	}
}
