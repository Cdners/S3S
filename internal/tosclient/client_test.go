package tosclient

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Cdners/S3S/internal/core"
)

const beijingHost = "tos-cn-beijing.volces.com"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func localTOS(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	keys := core.CloudCredentials{AccessKeyID: rand.Text(), SecretAccessKey: rand.Text(), SessionToken: rand.Text()}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "TOS4-HMAC-SHA256 Credential="+keys.AccessKeyID+"/") ||
			!strings.Contains(auth, "/cn-beijing/tos/request,") || !strings.Contains(auth, "Signature=") ||
			r.Header.Get("X-Tos-Security-Token") != keys.SessionToken || r.Header.Get("X-Tos-Date") == "" {
			t.Error("missing native regional signature or explicit STS credentials")
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	peer, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	// Redirect only the network dial to this test's TLS server. Keep the
	// signed Host on the actual HTTP request received by the fixture.
	tr := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		copy := req.Clone(req.Context())
		copy.Host = req.URL.Host
		copy.URL.Host = peer.Host
		return server.Client().Transport.RoundTrip(copy)
	})
	c, err := New(core.Connection{ID: "c", Name: "TOS", Provider: core.TOS, Region: "cn-beijing"}, keys, tr)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRegionAndDestinationValidation(t *testing.T) {
	keys := core.CloudCredentials{AccessKeyID: rand.Text(), SecretAccessKey: rand.Text()}
	for _, tc := range []struct {
		region, endpoint string
		valid            bool
	}{
		{"cn-beijing", "", true}, {"", "HTTPS://TOS-CN-BEIJING.VOLCES.COM:443/", true}, {"", "https://" + beijingHost + ":8443", false}, {"", "https://" + beijingHost + ":", false}, {"", "https://" + beijingHost + "/", true}, {"cn-shanghai", "https://tos-cn-shanghai.volces.com", true},
		{"", "", false}, {"cn-shanghai", "https://" + beijingHost, false}, {"unknown", "", false},
		{"cn-beijing", "http://" + beijingHost, false}, {"cn-beijing", "https://" + beijingHost + ".example.test", false},
		{"cn-beijing", "https://" + beijingHost + "/bucket", false}, {"cn-beijing", "https://user@" + beijingHost, false},
	} {
		_, err := New(core.Connection{ID: "id", Name: "test", Provider: core.TOS, Region: tc.region, Endpoint: tc.endpoint}, keys, nil)
		if (err == nil) != tc.valid {
			t.Errorf("region %q endpoint %q: %v", tc.region, tc.endpoint, err)
		}
	}
	t.Setenv("TOS_ACCESS_KEY", "ambient-test")
	t.Setenv("TOS_SECRET_KEY", "ambient-test")
	if _, err := New(core.Connection{ID: "c", Name: "test", Provider: core.TOS, Region: "cn-beijing"}, core.CloudCredentials{}, nil); err == nil {
		t.Fatal("accepted missing explicit credentials")
	}
}

func TestBucketPaginationAndSignedObjectBrowsing(t *testing.T) {
	const key = " /a//../中文+%2F/./界\x01 "
	const prefix = " /a//../"
	c := localTOS(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("unexpected method")
			w.WriteHeader(405)
			return
		}
		switch r.Host {
		case beijingHost:
			if r.URL.Path != "/" || r.URL.RawQuery != "" {
				t.Error("unexpected bucket-list request")
			}
			var raw []map[string]string
			for i := 100; i >= 0; i-- {
				raw = append(raw, map[string]string{"Name": fmt.Sprintf("bucket-%03d", i), "Location": "cn-beijing"})
			}
			json.NewEncoder(w).Encode(map[string]any{"Buckets": raw})
		case "data." + beijingHost:
			q := r.URL.Query()
			if q.Get("list-type") != "2" || q.Get("prefix") != prefix || q.Get("delimiter") != "/" || q.Get("max-keys") != "100" {
				t.Errorf("unexpected object query: %s", r.URL.RawQuery)
			}
			switch q.Get("continuation-token") {
			case "":
				json.NewEncoder(w).Encode(map[string]any{"Contents": []map[string]any{{"Key": key, "Size": 7, "ETag": `"a"`}}, "CommonPrefixes": []map[string]string{{"Prefix": prefix + " +%2F/"}}, "IsTruncated": true, "NextContinuationToken": "T+/="})
			case "T+/=":
				io.WriteString(w, `{"Contents":[],"IsTruncated":false}`)
			default:
				t.Error("cursor changed")
			}
		default:
			t.Error("unexpected signing host")
		}
	})
	ctx := t.Context()
	first, err := c.ListBuckets(ctx, "")
	if err != nil || len(first.Buckets) != 100 || first.Buckets[0] != "bucket-000" || first.NextToken != "bucket-099" {
		t.Fatalf("invalid first bucket page: %v", err)
	}
	rest, err := c.ListBuckets(ctx, first.NextToken)
	if err != nil || len(rest.Buckets) != 1 || rest.Buckets[0] != "bucket-100" || rest.NextToken != "" {
		t.Fatalf("invalid second bucket page: %v", err)
	}
	page, err := c.ListObjects(ctx, "data", prefix, "")
	if err != nil || len(page.Objects) != 1 || page.Objects[0].Key != key || page.Objects[0].Size != 7 || len(page.Prefixes) != 1 || page.Prefixes[0] != prefix+" +%2F/" || page.NextToken != "T+/=" {
		t.Fatalf("lost key, prefix or cursor: %#v %v", page, err)
	}
	last, err := c.ListObjects(ctx, "data", prefix, page.NextToken)
	if err != nil || len(last.Objects) != 0 || last.NextToken != "" {
		t.Fatalf("invalid final page: %v", err)
	}
}

func TestDownloadPreservesPathAndPinsVersion(t *testing.T) {
	const key = "/a//../?+#文/%2F/./\x01 "
	c := localTOS(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "data."+beijingHost || r.URL.Path != "/"+key {
			t.Errorf("changed object path %q", r.URL.Path)
		}
		w.Header().Set("ETag", `"opaque-v"`)
		w.Header().Set("x-tos-version-id", "v+/=")
		if r.Method == "HEAD" {
			w.Header().Set("Content-Length", "5")
			return
		}
		if r.Method != "GET" || r.Header.Get("Range") != "bytes=1-3" || r.Header.Get("If-Match") != `"opaque-v"` || r.URL.Query().Get("versionId") != "v+/=" {
			t.Error("range request lost its identity")
		}
		w.Header().Set("Content-Length", "3")
		w.Header().Set("Content-Range", "bytes 1-3/5")
		w.WriteHeader(206)
		io.WriteString(w, "bcd")
	})
	ctx := t.Context()
	id, err := c.Head(ctx, "data", key)
	if err != nil || id.Size != 5 || id.ETag != `"opaque-v"` || id.VersionID != "v+/=" {
		t.Fatalf("identity: %+v %v", id, err)
	}
	body, err := c.ReadRange(ctx, "data", key, id, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil || string(data) != "bcd" {
		t.Fatalf("range: %q %v", data, err)
	}
}

func TestRefuseInvalidRangesAndErrorMetadata(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		status                   int
		contentRange, etag, code string
	}{
		{"ignored", 200, "bytes 1-2/4", `"v"`, "error.cloudRange"},
		{"shifted", 206, "bytes 0-1/4", `"v"`, "error.cloudRange"},
		{"different version", 206, "bytes 1-2/4", `"other"`, "error.cloudChanged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := localTOS(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("ETag", tc.etag)
				w.Header().Set("Content-Length", "2")
				w.Header().Set("Content-Range", tc.contentRange)
				w.WriteHeader(tc.status)
				io.WriteString(w, "xy")
			})
			body, err := c.ReadRange(t.Context(), "data", "key", core.ObjectIdentity{Size: 4, ETag: `"v"`}, 1, 2)
			if body != nil || err == nil || err.Error() != tc.code {
				t.Fatalf("unsafe range accepted: %v", err)
			}
		})
	}
	var followed atomic.Bool
	c := localTOS(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			followed.Store(true)
			return
		}
		if r.URL.Query().Get("prefix") == "redirect" {
			http.Redirect(w, r, "https://data."+beijingHost+"/redirected", 307)
			return
		}
		w.Header().Set("x-tos-request-id", "test-req-22")
		w.WriteHeader(403)
		io.WriteString(w, `{"Code":"AccessDenied","Message":"sensitive response context","RequestId":"test-req-22"}`)
	})
	_, err := c.ListObjects(t.Context(), "data", "", "")
	var detail *core.Error
	if !errors.As(err, &detail) || detail.Code != "error.cloudAccess" || detail.ProviderCode != "AccessDenied" || detail.RequestID != "test-req-22" {
		t.Fatalf("unexpected error: %#v", err)
	}
	jsonBytes, _ := json.Marshal(detail)
	if strings.Contains(string(jsonBytes), "sensitive") {
		t.Fatal("leaked vendor message")
	}
	if _, err := c.ListObjects(t.Context(), "data", "redirect", ""); err == nil || followed.Load() {
		t.Fatal("followed storage redirect")
	}
	if _, err := c.ListObjects(t.Context(), "attacker.example", "", ""); err == nil {
		t.Fatal("accepted invalid bucket")
	}
}
