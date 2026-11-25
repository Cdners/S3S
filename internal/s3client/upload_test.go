package s3client

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cdners/S3S/internal/core"
)

func checksumMD5(b []byte) string {
	sum := md5.Sum(b)
	return base64.StdEncoding.EncodeToString(sum[:])
}

func TestMultipartWireContract(t *testing.T) {
	const key = "/prefix//../文+%2F "
	const task = "task-743"
	const uploadID = "session/+=!"
	const etag = `"opaque-not-an-md5"`
	payload := []byte("part content")
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	for _, provider := range []core.Provider{core.AWS, core.R2} {
		t.Run(string(provider), func(t *testing.T) {
			var aborted atomic.Bool
			client := wireClient(t, provider, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/bucket/"+key || r.Header.Get("Authorization") == "" {
					t.Error("object key changed or signature missing")
				}
				q := r.URL.Query()
				switch {
				case r.Method == "POST" && q.Has("uploads"):
					if r.Header.Get("x-amz-meta-s3s-task") != task || r.Header.Get("x-amz-meta-s3s-sha256") != digest {
						t.Error("missing operation identity")
					}
					fmt.Fprintf(w, "<InitiateMultipartUploadResult><UploadId>%s</UploadId></InitiateMultipartUploadResult>", uploadID)
				case q.Get("uploadId") != uploadID:
					t.Error("lost session identifier")
					w.WriteHeader(400)
				case r.Method == "PUT":
					data, err := io.ReadAll(r.Body)
					if err != nil || !bytes.Equal(data, payload) || r.ContentLength != int64(len(payload)) || q.Get("partNumber") != "1" ||
						r.Header.Get("Content-Md5") != checksumMD5(data) {
						t.Error("part body, range, or checksum changed")
					}
					w.Header().Set("ETag", etag)
				case r.Method == "GET":
					if aborted.Load() {
						w.WriteHeader(404)
						io.WriteString(w, "<Error><Code>NoSuchUpload</Code></Error>")
						return
					}
					if q.Get("max-parts") != "1000" {
						t.Error("missing part page size")
					}
					marker := q.Get("part-number-marker")
					switch marker {
					case "":
						fmt.Fprintf(w, "<ListPartsResult><Part><PartNumber>1</PartNumber><Size>%d</Size><ETag>%s</ETag></Part><IsTruncated>true</IsTruncated><NextPartNumberMarker>1</NextPartNumberMarker></ListPartsResult>", len(payload), etag)
					case "1":
						io.WriteString(w, `<ListPartsResult><Part><PartNumber>2</PartNumber><Size>1</Size><ETag>"second"</ETag></Part><IsTruncated>false</IsTruncated></ListPartsResult>`)
					default:
						t.Errorf("unexpected marker %q", marker)
					}
				case r.Method == "POST":
					if r.Header.Get("If-None-Match") != "*" {
						t.Error("unconditional completion")
					}
					var completion struct {
						Parts []struct {
							PartNumber int
							ETag       string
						} `xml:"Part"`
					}
					err := xml.NewDecoder(r.Body).Decode(&completion)
					if err != nil || len(completion.Parts) != 2 || completion.Parts[0].PartNumber != 1 || completion.Parts[0].ETag != etag ||
						completion.Parts[1].PartNumber != 2 || completion.Parts[1].ETag != `"second"` {
						t.Error("incorrect completion manifest")
					}
					io.WriteString(w, `<CompleteMultipartUploadResult><ETag>"done"</ETag></CompleteMultipartUploadResult>`)
				case r.Method == "DELETE":
					aborted.Store(true)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected method %s", r.Method)
				}
			})
			ctx := t.Context()
			id, err := client.BeginUpload(ctx, "bucket", key, task, digest)
			if err != nil || id != uploadID {
				t.Fatalf("initiate: %v", err)
			}
			tag, err := client.UploadPart(ctx, "bucket", key, id, 1, bytes.NewReader(payload), int64(len(payload)), checksumMD5(payload))
			if err != nil || tag != etag {
				t.Fatalf("upload: %v", err)
			}
			parts, err := client.ListParts(ctx, "bucket", key, id)
			if err != nil || len(parts) != 2 || parts[0].ETag != tag || parts[0].Size != int64(len(payload)) || parts[1].Number != 2 {
				t.Fatalf("remote parts: %+v %v", parts, err)
			}
			if err := client.CompleteUpload(ctx, "bucket", key, id, parts); err != nil {
				t.Fatal(err)
			}
			if err := client.AbortUpload(ctx, "bucket", key, id); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEmptyPutAndPublicationIdentity(t *testing.T) {
	const task = "task-one"
	client := wireClient(t, core.R2, func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/bucket/")
		if r.Method == "PUT" {
			b, err := io.ReadAll(r.Body)
			if err != nil || len(b) != 0 || r.ContentLength != 0 || r.Header.Get("Content-MD5") != emptyMD5Base64 || r.Header.Get("If-None-Match") != "*" ||
				r.Header.Get("x-amz-meta-s3s-task") != task || r.Header.Get("x-amz-meta-s3s-sha256") != emptySHA256 {
				t.Error("unsafe empty upload request")
			}
			return
		}
		if r.Method != "HEAD" {
			t.Errorf("unexpected method %s", r.Method)
		}
		switch key {
		case "absent":
			w.WriteHeader(404)
			return
		case "forbidden":
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Length", "0")
		w.Header().Set("x-amz-meta-s3s-task", task)
		w.Header().Set("x-amz-meta-s3s-sha256", emptySHA256)
		switch key {
		case "different-task":
			w.Header().Set("x-amz-meta-s3s-task", "someone-else")
		case "different-digest":
			w.Header().Set("x-amz-meta-s3s-sha256", strings.Repeat("1", 64))
		case "different-size":
			w.Header().Set("Content-Length", "1")
		}
	})
	ctx := t.Context()
	if err := client.PutEmpty(ctx, "bucket", "empty", task, emptySHA256); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		key  string
		ok   bool
		code string
	}{
		{"empty", true, ""}, {"absent", false, ""}, {"different-size", false, "error.uploadExists"}, {"different-task", false, "error.uploadExists"},
		{"different-digest", false, "error.uploadExists"}, {"forbidden", false, "error.cloudAccess"},
	} {
		ok, err := client.VerifyUpload(ctx, "bucket", tc.key, task, emptySHA256, 0)
		if ok != tc.ok || (err == nil) != (tc.code == "") || err != nil && err.Error() != tc.code {
			t.Errorf("%s: %v %v", tc.key, ok, err)
		}
	}
}

func TestUploadConditionalErrorsNeverDowngrade(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		providerCode string
		want         core.ErrorCode
	}{
		{"existing", 412, "PreconditionFailed", "error.uploadExists"},
		{"concurrent", 409, "ConditionalRequestConflict", core.Conflict},
		{"unsupported", 501, "NotImplemented", "error.cloudRequest"},
		{"embedded failure", 200, "NoSuchUpload", "error.uploadSessionMissing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client := wireClient(t, core.AWS, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("If-None-Match") != "*" {
					t.Error("lost condition")
				}
				w.Header().Set("x-amz-request-id", "req-test")
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, "<Error><Code>%s</Code><Message>private text</Message></Error>", tc.providerCode)
			})
			err := client.CompleteUpload(t.Context(), "bucket", "key", "u", []core.Part{{Number: 1, ETag: `"tag"`}})
			var detail *core.Error
			if !errors.As(err, &detail) || detail.Code != tc.want || detail.ProviderCode != tc.providerCode || detail.RequestID != "req-test" || calls.Load() != 1 {
				t.Fatalf("unexpected error or retried request: %#v (calls=%d)", err, calls.Load())
			}
		})
	}
}

func TestAbortMissingSessionAndRejectBrokenPage(t *testing.T) {
	client := wireClient(t, core.R2, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("uploadId") == "missing" {
			w.WriteHeader(404)
			io.WriteString(w, "<Error><Code>NoSuchUpload</Code></Error>")
			return
		}
		if r.Method == "DELETE" {
			w.WriteHeader(403)
			io.WriteString(w, "<Error><Code>AccessDenied</Code></Error>")
			return
		}
		io.WriteString(w, `<ListPartsResult><Part><PartNumber>1</PartNumber><ETag>"a"</ETag><Size>1</Size></Part><IsTruncated>true</IsTruncated><NextPartNumberMarker>0</NextPartNumberMarker></ListPartsResult>`)
	})
	ctx := t.Context()
	if err := client.AbortUpload(ctx, "bucket", "key", "missing"); err != nil {
		t.Fatal(err)
	}
	if err := client.AbortUpload(ctx, "bucket", "key", "denied"); err == nil || err.Error() != "error.cloudAccess" {
		t.Fatalf("masked abort error: %v", err)
	}
	if parts, err := client.ListParts(ctx, "bucket", "key", "missing"); parts != nil || err == nil || err.Error() != "error.uploadSessionMissing" {
		t.Fatalf("missing session: %v", err)
	}
	if parts, err := client.ListParts(ctx, "bucket", "key", "cyclic"); parts != nil || err == nil {
		t.Fatal("accepted a non-advancing page")
	}
}

func TestUploadPartRejectsBadLengthAndCanceledWork(t *testing.T) {
	client := wireClient(t, core.AWS, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid request sent") })
	ctx := t.Context()
	payload := []byte("abc")
	for _, tc := range []struct {
		number int
		size   int64
		md5    string
	}{
		{0, 3, checksumMD5(payload)}, {maxParts + 1, 3, checksumMD5(payload)},
		{1, 2, checksumMD5(payload)}, {1, 3, "wrong"}, {1, maxPartBytes + 1, checksumMD5(payload)},
	} {
		_, err := client.UploadPart(ctx, "bucket", "key", "u", tc.number, bytes.NewReader(payload), tc.size, tc.md5)
		if err == nil || err.Error() != string(core.InvalidPart) {
			t.Fatalf("bad input accepted: %v", err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := client.BeginUpload(canceled, "bucket", "key", "task", emptySHA256); err == nil {
		t.Fatal("canceled create succeeded")
	}
}

func TestAbortRequiresSessionDisappearance(t *testing.T) {
	client := wireClient(t, core.AWS, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "DELETE":
			w.WriteHeader(http.StatusNoContent)
		case "GET":
			if r.URL.Query().Get("uploadId") == "empty" {
				io.WriteString(w, "<ListPartsResult><IsTruncated>false</IsTruncated></ListPartsResult>")
				return
			}
			io.WriteString(w, `<ListPartsResult><IsTruncated>false</IsTruncated><Part><PartNumber>1</PartNumber><ETag>"late"</ETag><Size>8</Size></Part></ListPartsResult>`)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	})
	for _, id := range []string{"upload", "empty"} {
		err := client.AbortUpload(t.Context(), "bucket", "key", id)
		var e *core.Error
		if !errors.As(err, &e) || e.Code != "error.uploadCleanup" || !e.Retryable {
			t.Fatalf("cleanup error for %s: %#v", id, err)
		}
	}
}

func TestUploadRejectsRedirectAndHonorsCancel(t *testing.T) {
	destination := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a signed upload escaped its origin")
	}))
	defer destination.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	client := wireClient(t, core.R2, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bucket/redirect":
			http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
		case "/bucket/wait":
			close(started)
			select {
			case <-r.Context().Done():
			case <-release:
			}
		default:
			t.Error("unexpected key")
		}
	})
	defer close(release)
	if err := client.PutEmpty(t.Context(), "bucket", "redirect", "task", emptySHA256); err == nil {
		t.Fatal("accepted redirected write")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		b := []byte("part")
		_, err := client.UploadPart(ctx, "bucket", "wait", "u", 1, bytes.NewReader(b), int64(len(b)), checksumMD5(b))
		done <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("part did not reach server")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled part was reported successful")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not interrupt part request")
	}
}
