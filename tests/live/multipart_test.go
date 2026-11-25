//go:build liveupload

package live

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/Cdners/S3S/internal/core"
)

// Exercise write permissions and cleanup on a dedicated bucket without publishing an object.
func TestAbortMultipartFixture(t *testing.T) {
	client := fixtureClient(t)
	bucket := required(t, "S3S_TEST_BUCKET")
	marker := rand.Text()
	key := "s3s-acceptance/" + marker
	payload := make([]byte, 5<<20)
	rand.Read(payload)
	digest := sha256.Sum256(payload)
	checksum := md5.Sum(payload) // Content-MD5 wire integrity field
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()

	id, err := client.BeginUpload(ctx, bucket, key, marker, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatalf("begin upload at %s: %v", key, err)
	}
	// A failing assertion or an expired upload deadline must still try cleanup.
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := client.AbortUpload(cleanup, bucket, key, id); err != nil {
			t.Errorf("abort incomplete upload at %s: %v", key, err)
			return
		}
		_, err := client.ListParts(cleanup, bucket, key, id)
		var detail *core.Error
		if !errors.As(err, &detail) || detail.Code != "error.uploadSessionMissing" {
			t.Errorf("removal of multipart session at %s was not confirmed", key)
		}
	})
	etag, err := client.UploadPart(ctx, bucket, key, id, 1, bytes.NewReader(payload), int64(len(payload)), base64.StdEncoding.EncodeToString(checksum[:]))
	if err != nil {
		t.Fatal(err)
	}
	parts, err := client.ListParts(ctx, bucket, key, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0].Number != 1 || parts[0].ETag != etag || parts[0].Size != int64(len(payload)) {
		t.Fatal("remote part differs from the acknowledged upload")
	}
}
