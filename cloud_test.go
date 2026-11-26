package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/vault"
	"github.com/zalando/go-keyring"
)

// Simulate a bucket-only IAM policy at the HTTP boundary. Every request passes
// through the actual SDK signer; the transport does not open a network socket.
func TestProbeBucketOnlyPolicyAndMetadata(t *testing.T) {
	keyring.MockInit()
	w := openWorkspaceAt(t.TempDir())
	defer w.close()
	ctx := t.Context()
	keys := accountSecrets()
	saved := w.SaveAccount(ctx, nil, core.Connection{Name: "limited", Provider: core.AWS, Region: "us-west-2"}, &keys)
	if saved.Error != nil {
		t.Fatal(saved.Error)
	}
	c := *saved.Data
	key := "a//../中文 %"
	var paths []string
	w.storageTransport = rejectUploadRequest(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		if r.URL.Host != "s3.us-west-2.amazonaws.com" || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("expected scoped authenticated request")
		}
		h := http.Header{}
		h.Set("X-Amz-Request-Id", "request-123")
		var body string
		status := 200
		var size int64
		switch {
		case r.Method == "GET" && r.URL.Path == "/":
			status = 403
			body = "<Error><Code>AccessDenied</Code><Message>private vendor body</Message></Error>"
		case r.Method == "GET" && r.URL.Path == "/known" && r.URL.Query().Get("list-type") == "2":
			deadline, ok := r.Context().Deadline()
			if !ok || time.Until(deadline) > 12*time.Second {
				t.Error("probe lacks network deadline")
			}
			body = "<ListBucketResult><Name>known</Name><IsTruncated>false</IsTruncated></ListBucketResult>"
		case r.Method == "HEAD" && r.URL.Path == "/known/"+key:
			size = 42
			h.Set("Content-Length", "42")
			h.Set("ETag", "\"obj\"")
			h.Set("X-Amz-Version-Id", "rev")
		default:
			t.Errorf("unexpected read operation %s %q", r.Method, r.URL.Path)
			status = 400
		}
		if r.Method != "HEAD" {
			size = int64(len(body))
		}
		return &http.Response{StatusCode: status, Header: h, ContentLength: size, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	account := w.TestConnection(ctx, c, "")
	if account.Error == nil || account.Error.Code != "error.cloudAccess" || account.Error.ProviderCode != "AccessDenied" ||
		account.Error.RequestID != "request-123" || account.Data == nil || account.Data.Operation != "listBuckets" {
		t.Fatalf("unexpected account probe %#v", account.Error)
	}
	wire, err := json.Marshal(account)
	if err != nil || strings.Contains(string(wire), "private vendor body") || strings.Contains(string(wire), keys.AccessKeyID) {
		t.Fatal("probe leaked diagnostics")
	}
	bucket := w.TestConnection(ctx, c, "known")
	if bucket.Error != nil || bucket.Data == nil || bucket.Data.Operation != "listObjects" || bucket.Data.LatencyMS < 0 {
		t.Fatalf("bucket probe: %v", bucket.Error)
	}
	meta := w.ObjectMetadata(ctx, c, "known", key)
	if meta.Error != nil || meta.Data == nil || *meta.Data != (core.ObjectIdentity{Size: 42, ETag: "\"obj\"", VersionID: "rev"}) {
		t.Fatalf("unexpected signed HEAD result: %v", meta.Error)
	}
	changed := c
	changed.Region = "us-east-1"
	if err := w.store.SaveConnection(ctx, changed); err != nil {
		t.Fatal(err)
	}
	stale := w.TestConnection(ctx, c, "known")
	if stale.Error == nil || stale.Error.Code != "error.connectionChanged" {
		t.Fatal("stale probe accepted")
	}
	missing := w.TestConnection(ctx, changed, "known")
	if missing.Error == nil || missing.Error.Code != "error.credentialsMissing" {
		t.Fatal("retargeted account reused old credential")
	}
	if len(paths) != 3 {
		t.Fatalf("expected exactly 3 signed requests, got %v", paths)
	}
}

func TestProbeDeadline(t *testing.T) {
	keyring.MockInit()
	w := openWorkspaceAt(t.TempDir())
	defer w.close()
	keys := accountSecrets()
	saved := w.SaveAccount(t.Context(), nil, core.Connection{Name: "slow", Provider: core.AWS}, &keys)
	if saved.Error != nil {
		t.Fatal(saved.Error)
	}
	w.storageTransport = rejectUploadRequest(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	result := w.TestConnection(ctx, *saved.Data, "")
	if result.Error == nil || result.Error.Code != "error.cloudTimeout" || !result.Error.Retryable || result.Data == nil {
		t.Fatal("deadline did not produce a safe retryable timeout")
	}
}

func TestReadOnlyProvidersRejectUpload(t *testing.T) {
	w := openWorkspaceAt(t.TempDir())
	defer w.close()
	w.chooseSource = func(string) (string, error) {
		t.Fatal("backend opened upload picker for read-only provider")
		return "", nil
	}
	caps := w.Capabilities().Data
	for _, provider := range []core.Provider{core.OSS, core.TOS, core.S3Compatible} {
		c := core.Connection{Name: "read", Provider: provider}
		if cap := caps[provider]; !cap.ListBuckets || !cap.ListObjects || !cap.Metadata || !cap.Download || cap.Upload {
			t.Fatalf("bad read-only capabilities: %+v", cap)
		}
		r := w.StartUpload(t.Context(), c, "bucket", "key", "ignored")
		if r.Error == nil || r.Error.Code != "error.cloudProvider" {
			t.Fatalf("unsafe upload allowed for %s", provider)
		}
		if _, err := w.destination(t.Context(), c); err == nil || err.Error() != "error.cloudProvider" {
			t.Fatalf("unsafe restore destination for %s", provider)
		}
	}
	if !caps[core.AWS].Upload || !caps[core.R2].Upload {
		t.Fatal("existing upload adapters disabled")
	}
}

// Existing profiles already use the scoped keyring format. Reopen SQLite, then
// exercise each new adapter through the public bridge without rewriting either.
func TestExistingReadOnlyProfilesDispatchNativeSigners(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	xmlPage := "<ListBucketResult><Name>known</Name><IsTruncated>false</IsTruncated></ListBucketResult>"
	for _, tc := range []struct {
		c                           core.Connection
		host, path, algorithm, body string
	}{
		{core.Connection{ID: "oss", Name: "original", Provider: core.OSS, Region: "cn-hangzhou"}, "known.oss-cn-hangzhou.aliyuncs.com", "/", "OSS4-HMAC-SHA256", xmlPage},
		{core.Connection{ID: "tos", Name: "original", Provider: core.TOS, Region: "cn-beijing"}, "known.tos-cn-beijing.volces.com", "/", "TOS4-HMAC-SHA256", `{"Contents":[],"IsTruncated":false}`},
		{core.Connection{ID: "custom", Name: "original", Provider: core.S3Compatible, Endpoint: "https://objects.example.test:9443"}, "objects.example.test:9443", "/known", "AWS4-HMAC-SHA256", xmlPage},
	} {
		t.Run(tc.c.ID, func(t *testing.T) {
			keys := accountSecrets()
			old := openWorkspaceAt(dir)
			if err := old.store.SaveConnection(t.Context(), tc.c); err != nil {
				t.Fatal(err)
			}
			if err := (vault.Vault{}).Save(tc.c, keys); err != nil {
				t.Fatal(err)
			}
			old.close()
			w := openWorkspaceAt(dir)
			defer w.close()
			calls := 0
			w.storageTransport = rejectUploadRequest(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "GET" || r.URL.Scheme != "https" || r.URL.Host != tc.host || r.URL.EscapedPath() != tc.path || r.URL.Query().Get("list-type") != "2" {
					t.Errorf("unexpected bucket-scoped target %s %s", r.Method, r.URL.Redacted())
				}
				if !strings.HasPrefix(r.Header.Get("Authorization"), tc.algorithm+" Credential="+keys.AccessKeyID+"/") {
					t.Error("missing expected native signer and account key")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, ContentLength: int64(len(tc.body)), Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})
			result := w.TestConnection(t.Context(), tc.c, "known")
			if calls != 1 || result.Error != nil || result.Data == nil || result.Data.Operation != "listObjects" {
				t.Fatalf("unexpected probe, calls=%d error=%v", calls, result.Error)
			}
		})
	}
}

// The previous metadata-only adapters accepted HTTP and nonnative endpoints.
// Upgrading must keep an old profile editable without sending its stored keys.
func TestLegacyIncompatibleProfilesFailBeforeNetwork(t *testing.T) {
	keyring.MockInit()
	w := openWorkspaceAt(t.TempDir())
	defer w.close()
	w.storageTransport = rejectUploadRequest(func(*http.Request) (*http.Response, error) {
		t.Fatal("legacy credentials reached the network")
		return nil, nil
	})
	for _, c := range []core.Connection{
		{ID: "old-oss", Name: "legacy", Provider: core.OSS, Endpoint: "https://foreign.example", Region: "cn-hangzhou"},
		{ID: "old-tos", Name: "legacy", Provider: core.TOS, Endpoint: "https://foreign.example", Region: "cn-beijing"},
		{ID: "old-s3", Name: "legacy", Provider: core.S3Compatible, Endpoint: "http://legacy.example"},
	} {
		if err := w.store.SaveConnection(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		if err := (vault.Vault{}).Save(c, accountSecrets()); err != nil {
			t.Fatal(err)
		}
		probe := w.TestConnection(t.Context(), c, "known")
		if probe.Error == nil || probe.Error.Code != core.InvalidConnection || probe.Data != nil {
			t.Fatalf("incompatible %s profile accepted", c.Provider)
		}
		loaded, err := w.store.Connection(t.Context(), c.ID)
		if err != nil || loaded != c {
			t.Fatalf("legacy %s profile changed", c.Provider)
		}
	}
}
