package ossclient

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Cdners/S3S/internal/core"
)

const (
	region      = "cn-hangzhou"
	serviceHost = "oss-cn-hangzhou.aliyuncs.com"
	bucket      = "test-fixture"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Recompute OSS4 from the received method, resource, query, signed headers,
// and temporary credentials, independent of the SDK signer.
func verifySignature(t *testing.T, r *http.Request, k core.CloudCredentials) {
	t.Helper()
	stamp := r.Header.Get("X-Oss-Date")
	if len(stamp) != 16 || r.Header.Get("X-Oss-Security-Token") != k.SessionToken {
		t.Error("missing timestamp or selected session token")
		return
	}
	uri := r.URL.EscapedPath()
	if r.Host != serviceHost {
		uri = "/" + strings.TrimSuffix(r.Host, "."+serviceHost) + uri
	}
	var headers []string
	for name, values := range r.Header {
		lower := strings.ToLower(name)
		if lower == "content-type" || lower == "content-md5" || strings.HasPrefix(lower, "x-oss-") {
			headers = append(headers, lower+":"+strings.Join(values, ",")+"\n")
		}
	}
	sort.Strings(headers)
	// OSS omits '=' after empty values in the canonical query string.
	var params []string
	for _, pair := range strings.Split(strings.ReplaceAll(r.URL.RawQuery, "+", "%20"), "&") {
		if pair != "" {
			params = append(params, strings.TrimSuffix(pair, "="))
		}
	}
	sort.Strings(params)
	canonical := strings.Join([]string{r.Method, uri, strings.Join(params, "&"), strings.Join(headers, ""), "", r.Header.Get("X-Oss-Content-Sha256")}, "\n")
	scope := stamp[:8] + "/" + region + "/oss/aliyun_v4_request"
	toSign := fmt.Sprintf("OSS4-HMAC-SHA256\n%s\n%s\n%x", stamp, scope, sha256.Sum256([]byte(canonical)))
	key := []byte("aliyun_v4" + k.SecretAccessKey)
	for _, step := range append(strings.Split(scope, "/"), toSign) {
		h := hmac.New(sha256.New, key)
		io.WriteString(h, step)
		key = h.Sum(nil)
	}
	expected := "OSS4-HMAC-SHA256 Credential=" + k.AccessKeyID + "/" + scope + ",Signature=" + hex.EncodeToString(key)
	if !hmac.Equal([]byte(r.Header.Get("Authorization")), []byte(expected)) {
		t.Error("OSS4 request signature did not match the received request")
	}
}

func wireClient(t *testing.T, handle http.HandlerFunc) *Client {
	t.Helper()
	keys := core.CloudCredentials{AccessKeyID: rand.Text(), SecretAccessKey: rand.Text(), SessionToken: rand.Text()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifySignature(t, r, keys)
		handle(w, r)
	}))
	t.Cleanup(srv.Close)
	addr, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	// Redirect the connection to the in-process test listener only after the
	// production transport has validated the actual signed scheme and host.
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		copy.Host = r.URL.Host
		copy.URL.Host, copy.URL.Scheme = addr.Host, addr.Scheme
		return srv.Client().Transport.RoundTrip(copy)
	})
	c, err := New(core.Connection{ID: "account", Name: "test", Region: region, Provider: core.OSS}, keys, transport)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBucketMarkerAndScopedObjects(t *testing.T) {
	const prefix = "a//../+文/"
	const rawKey = prefix + "x %2F/./#?"
	c := wireClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != "GET" {
			t.Errorf("unexpected method %s", r.Method)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		if r.Host == serviceHost {
			if q.Get("max-keys") != "100" {
				t.Error("expected bounded bucket listing")
			}
			if !q.Has("marker") {
				io.WriteString(w, `<ListAllMyBucketsResult><IsTruncated>true</IsTruncated><NextMarker>after-1</NextMarker><Buckets><Bucket><Name>first-bucket</Name></Bucket></Buckets></ListAllMyBucketsResult>`)
			} else {
				if q.Get("marker") != "after-1" {
					t.Error("unexpected bucket cursor")
				}
				io.WriteString(w, `<ListAllMyBucketsResult><IsTruncated>false</IsTruncated><Buckets><Bucket><Name>second-bucket</Name></Bucket></Buckets></ListAllMyBucketsResult>`)
			}
			return
		}
		if r.Host != bucket+"."+serviceHost || r.URL.Path != "/" || q.Get("prefix") != prefix || q.Get("delimiter") != "/" ||
			q.Get("encoding-type") != "url" || q.Get("list-type") != "2" || q.Get("max-keys") != "100" {
			t.Error("bad bucket-scoped listing")
		}
		if q.Get("continuation-token") != "" {
			if q.Get("continuation-token") != "next +/%" {
				t.Error("cursor changed")
			}
			io.WriteString(w, `<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`)
			return
		}
		fmt.Fprintf(w, `<ListBucketResult><IsTruncated>true</IsTruncated><EncodingType>url</EncodingType><NextContinuationToken>%s</NextContinuationToken><Contents><Key>%s</Key><Size>5</Size><ETag>"id"</ETag></Contents><CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes></ListBucketResult>`,
			url.QueryEscape("next +/%"), url.QueryEscape(rawKey), url.QueryEscape(prefix+"z/"))
	})
	ctx := t.Context()
	p, err := c.ListBuckets(ctx, "")
	if err != nil || len(p.Buckets) != 1 || p.Buckets[0] != "first-bucket" || p.NextToken != "after-1" {
		t.Fatalf("first bucket page: %+v %v", p, err)
	}
	p, err = c.ListBuckets(ctx, p.NextToken)
	if err != nil || len(p.Buckets) != 1 || p.Buckets[0] != "second-bucket" || p.NextToken != "" {
		t.Fatalf("second bucket page: %+v %v", p, err)
	}
	objects, err := c.ListObjects(ctx, bucket, prefix, "")
	if err != nil || len(objects.Objects) != 1 || objects.Objects[0].Key != rawKey || objects.Objects[0].Size != 5 || len(objects.Prefixes) != 1 || objects.Prefixes[0] != prefix+"z/" || objects.NextToken != "next +/%" {
		t.Fatalf("object page: %+v %v", objects, err)
	}
	objects, err = c.ListObjects(ctx, bucket, prefix, objects.NextToken)
	if err != nil || objects.NextToken != "" || len(objects.Objects) != 0 {
		t.Fatalf("final object page: %+v %v", objects, err)
	}
}

func TestPinnedRangeAndIdentity(t *testing.T) {
	const key = "/a//../%2F+雪/./?#"
	c := wireClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != bucket+"."+serviceHost || r.URL.Path != "/"+key {
			t.Errorf("wrong addressed key: %q", r.URL.Path)
		}
		w.Header().Set("ETag", `"v"`)
		w.Header().Set("x-oss-version-id", "version+/")
		if r.Method == "HEAD" {
			w.Header().Set("Content-Length", "5")
			return
		}
		if r.Method != "GET" || r.Header.Get("Range") != "bytes=1-3" || r.Header.Get("If-Match") != `"v"` ||
			r.URL.Query().Get("versionId") != "version+/" || r.Header.Get("x-oss-range-behavior") != "standard" {
			t.Error("incorrect pinned range request")
		}
		w.Header().Set("Content-Length", "3")
		w.Header().Set("Content-Range", "bytes 1-3/5")
		w.WriteHeader(206)
		io.WriteString(w, "bcd")
	})
	id, err := c.Head(t.Context(), bucket, key)
	if err != nil || id != (core.ObjectIdentity{Size: 5, ETag: `"v"`, VersionID: "version+/"}) {
		t.Fatalf("identity %+v %v", id, err)
	}
	body, err := c.ReadRange(t.Context(), bucket, key, id, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil || string(data) != "bcd" {
		t.Fatalf("range %q %v", data, err)
	}
}

func TestRefusesFullShiftedOrChangedRange(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		status                      int
		contentRange, etag, version string
		code                        core.ErrorCode
	}{
		{"whole object", 200, "", `"v"`, "one", "error.cloudRange"},
		{"shifted bytes", 206, "bytes 0-2/5", `"v"`, "one", "error.cloudRange"},
		{"changed etag", 206, "bytes 1-3/5", `"new"`, "one", "error.cloudChanged"},
		{"changed version", 206, "bytes 1-3/5", `"v"`, "two", "error.cloudChanged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := wireClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", tc.etag)
				w.Header().Set("Content-Range", tc.contentRange)
				w.Header().Set("x-oss-version-id", tc.version)
				w.Header().Set("Content-Length", "3")
				w.WriteHeader(tc.status)
				io.WriteString(w, "abc")
			})
			_, err := c.ReadRange(t.Context(), bucket, "key", core.ObjectIdentity{Size: 5, ETag: `"v"`, VersionID: "one"}, 1, 3)
			requireCode(t, err, tc.code)
		})
	}
}

func requireCode(t *testing.T, err error, code core.ErrorCode) *core.Error {
	t.Helper()
	var e *core.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("unexpected error: %v", err)
	}
	return e
}

func TestScopeRedirectAndSafeErrors(t *testing.T) {
	var followed atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Store(true) }))
	t.Cleanup(other.Close)
	c := wireClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("prefix") == "redirect" {
			http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.Header().Set("x-oss-request-id", "req42")
		w.WriteHeader(403)
		io.WriteString(w, `<Error><Code>AccessDenied</Code><RequestId>req42</RequestId><Message>do-not-display</Message></Error>`)
	})
	_, err := c.ListObjects(t.Context(), bucket, "redirect", "")
	requireCode(t, err, "error.cloudRequest")
	if followed.Load() {
		t.Fatal("followed a redirect")
	}
	_, err = c.ListObjects(t.Context(), bucket, "", "")
	e := requireCode(t, err, "error.cloudAccess")
	if e.RequestID != "req42" || e.ProviderCode != "AccessDenied" || strings.Contains(e.Error(), "do-not-display") {
		t.Fatal("unsafe or incomplete diagnostics")
	}
	_, err = c.ListObjects(t.Context(), "bad.host", "", "")
	requireCode(t, err, core.InvalidTask)
	cfg := core.Connection{ID: "c", Name: "test", Provider: core.OSS, Region: region}
	for _, endpoint := range []string{"http://" + serviceHost, "https://" + serviceHost + ".example.org", "https://oss-cn-beijing.aliyuncs.com", "https://user@" + serviceHost} {
		cfg.Endpoint = endpoint
		_, err := New(cfg, core.CloudCredentials{AccessKeyID: rand.Text(), SecretAccessKey: rand.Text()}, nil)
		requireCode(t, err, core.InvalidConnection)
	}
	cfg.Endpoint = ""
	_, err = New(cfg, core.CloudCredentials{}, nil)
	requireCode(t, err, "error.cloudCredentials")
	ctx := context.WithValue(t.Context(), expectedHostKey{}, serviceHost)
	req, err := http.NewRequestWithContext(ctx, "GET", "https://other.example.org", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (signedTransport{roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("sent signed headers to an unexpected host")
		return nil, nil
	})}).RoundTrip(req)
	if err == nil {
		t.Fatal("accepted unscoped request")
	}
}

func TestHistoricalOfficialEndpoints(t *testing.T) {
	keys := core.CloudCredentials{AccessKeyID: rand.Text(), SecretAccessKey: rand.Text()}
	for _, tc := range []struct{ region, endpoint, expected string }{
		{"", "HTTPS://OSS-CN-HANGZHOU.ALIYUNCS.COM:443/", serviceHost},
		{region, "https://oss-cn-hangzhou-internal.aliyuncs.com", "oss-cn-hangzhou-internal.aliyuncs.com"},
	} {
		c, err := New(core.Connection{ID: "test", Name: "old", Provider: core.OSS, Region: tc.region, Endpoint: tc.endpoint}, keys, nil)
		if err != nil || c.host != tc.expected {
			t.Fatalf("saved config: %s => %v", tc.endpoint, err)
		}
	}
}
