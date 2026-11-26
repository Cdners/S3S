package s3client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Cdners/S3S/internal/core"
)

// All endpoints used by the signing tests are local TLS servers with a test CA.
func wireClient(t *testing.T, provider core.Provider, serve http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewTLSServer(serve)
	t.Cleanup(server.Close)
	region := "us-east-1"
	if provider == core.R2 {
		region = "auto"
	}
	return newClient(provider, region, server.URL, Credentials{AccessKeyID: "fixture-id", SecretAccessKey: "fixture-signing-material", SessionToken: "fixture-session"}, server.Client().Transport)
}

func TestSignedBucketPages(t *testing.T) {
	client := wireClient(t, core.AWS, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Query().Get("max-buckets") != "100" || !strings.Contains(r.Header.Get("Authorization"), "/us-east-1/s3/aws4_request") || r.Header.Get("X-Amz-Security-Token") != "fixture-session" {
			t.Error("missing expected signed AWS list parameters")
		}
		switch r.URL.Query().Get("continuation-token") {
		case "":
			io.WriteString(w, "<ListAllMyBucketsResult><Buckets><Bucket><Name>one</Name></Bucket></Buckets><ContinuationToken>a+/=</ContinuationToken></ListAllMyBucketsResult>")
		case "a+/=":
			io.WriteString(w, "<ListAllMyBucketsResult><Buckets><Bucket><Name>two</Name></Bucket></Buckets></ListAllMyBucketsResult>")
		default:
			t.Error("wrong cursor")
		}
	})
	first, err := client.ListBuckets(t.Context(), "")
	if err != nil || len(first.Buckets) != 1 || first.Buckets[0] != "one" || first.NextToken != "a+/=" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	second, err := client.ListBuckets(t.Context(), first.NextToken)
	if err != nil || len(second.Buckets) != 1 || second.Buckets[0] != "two" || second.NextToken != "" {
		t.Fatalf("second page: %+v %v", second, err)
	}
}

func TestR2BasicListPages(t *testing.T) {
	client := wireClient(t, core.R2, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("max-buckets") || r.URL.Query().Has("continuation-token") || !strings.Contains(r.Header.Get("Authorization"), "/auto/s3/aws4_request") {
			t.Error("unsupported R2 parameters or signing region")
		}
		io.WriteString(w, "<ListAllMyBucketsResult><Buckets>")
		for i := 100; i >= 0; i-- {
			fmt.Fprintf(w, "<Bucket><Name>b%03d</Name></Bucket>", i)
		}
		io.WriteString(w, "</Buckets></ListAllMyBucketsResult>")
	})
	p, err := client.ListBuckets(t.Context(), "")
	if err != nil || len(p.Buckets) != 100 || p.NextToken != "b099" {
		t.Fatalf("first page count=%d token=%q error=%v", len(p.Buckets), p.NextToken, err)
	}
	p, err = client.ListBuckets(t.Context(), p.NextToken)
	if err != nil || len(p.Buckets) != 1 || p.Buckets[0] != "b100" || p.NextToken != "" {
		t.Fatalf("second page: %+v %v", p, err)
	}
}

func TestObjectKeysAndConditionalRange(t *testing.T) {
	const key = "/x//../+文/%2F/./\x01 "
	const prefix = "/x//../"
	client := wireClient(t, core.R2, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("list-type") == "2" {
			if q.Get("prefix") != prefix || q.Get("delimiter") != "/" || q.Get("encoding-type") != "url" || q.Get("continuation-token") != "tok+/" {
				t.Error("unexpected listing query")
			}
			fmt.Fprintf(w, `<ListBucketResult><EncodingType>url</EncodingType><IsTruncated>true</IsTruncated><NextContinuationToken>next+</NextContinuationToken><Contents><Key>%s</Key><Size>5</Size><ETag>"v"</ETag></Contents><CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes></ListBucketResult>`, url.PathEscape(key), url.PathEscape(prefix+"文/"))
			return
		}
		if r.URL.Path != "/bucket/"+key {
			t.Errorf("wire key changed: %q", r.URL.Path)
		}
		w.Header().Set("ETag", `"v"`)
		w.Header().Set("x-amz-version-id", "version+")
		if r.Method == "HEAD" {
			w.Header().Set("Content-Length", "5")
			return
		}
		if r.Method != "GET" || r.Header.Get("If-Match") != `"v"` || r.Header.Get("Range") != "bytes=1-3" || q.Get("versionId") != "version+" {
			t.Error("conditional request did not pin range/version")
		}
		w.Header().Set("Content-Length", "3")
		w.Header().Set("Content-Range", "bytes 1-3/5")
		w.WriteHeader(http.StatusPartialContent)
		io.WriteString(w, "bcd")
	})
	page, err := client.ListObjects(t.Context(), "bucket", prefix, "tok+/")
	if err != nil || len(page.Objects) != 1 || page.Objects[0].Key != key || page.Objects[0].Size != 5 || len(page.Prefixes) != 1 || page.Prefixes[0] != prefix+"文/" || page.NextToken != "next+" {
		t.Fatalf("wrong decoded page %+v %v", page, err)
	}
	id, err := client.Head(t.Context(), "bucket", page.Objects[0].Key)
	if err != nil || id.Size != 5 || id.ETag != `"v"` || id.VersionID != "version+" {
		t.Fatalf("head: %+v %v", id, err)
	}
	body, err := client.ReadRange(t.Context(), "bucket", key, id, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil || string(data) != "bcd" {
		t.Fatalf("range bytes: %q %v", data, err)
	}
}

func TestRangeRefusesChangedOrIgnoredResponse(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		status                   int
		contentRange, etag, code string
	}{
		{"full response", 200, "bytes 1-2/4", `"v"`, "error.cloudRange"},
		{"shifted range", 206, "bytes 0-1/4", `"v"`, "error.cloudRange"},
		{"wrong total", 206, "bytes 1-2/5", `"v"`, "error.cloudRange"},
		{"different object", 206, "bytes 1-2/4", `"changed"`, "error.cloudChanged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := wireClient(t, core.AWS, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("ETag", tc.etag)
				w.Header().Set("Content-Range", tc.contentRange)
				w.Header().Set("Content-Length", "2")
				w.WriteHeader(tc.status)
				io.WriteString(w, "xy")
			})
			body, err := client.ReadRange(t.Context(), "bucket", "key", core.ObjectIdentity{ETag: `"v"`, Size: 4}, 1, 2)
			if body != nil || err == nil || err.Error() != tc.code {
				t.Fatalf("unexpected range result %v", err)
			}
		})
	}
}

func TestEndpointAndCredentials(t *testing.T) {
	const account = "00000000000000000000000000000000"
	okCreds := Credentials{AccessKeyID: "example", SecretAccessKey: "fixture"}
	for _, tc := range []struct {
		provider         core.Provider
		endpoint, region string
		valid            bool
	}{
		{core.AWS, "", "", true}, {core.AWS, "https://s3.amazonaws.com", "us-east-1", true}, {core.AWS, "https://s3.cn-north-1.amazonaws.com.cn", "cn-north-1", true},
		{core.R2, "https://" + account + ".eu.r2.cloudflarestorage.com", "auto", true},
		{core.R2, "https://" + account + ".us.r2.cloudflarestorage.com", "auto", true},
		{core.R2, "https://" + account + ".fedramp.r2.cloudflarestorage.com", "auto", true},
		{core.R2, "https://" + account + ".unknown.r2.cloudflarestorage.com", "auto", false},
		{core.R2, "https://" + account + ".us.r2.cloudflarestorage.com.example.org", "auto", false},
		{core.R2, "", "", false}, {core.R2, "http://" + account + ".r2.cloudflarestorage.com", "", false}, {core.R2, "https://example.com", "", false},
		{core.AWS, "https://example.com", "us-east-1", false}, {core.AWS, "", "../../untrusted", false}, {core.OSS, "", "", false},
	} {
		_, err := New(core.Connection{ID: "id", Name: "name", Provider: tc.provider, Endpoint: tc.endpoint, Region: tc.region}, okCreds)
		if (err == nil) != tc.valid {
			t.Errorf("endpoint %q region %q: %v", tc.endpoint, tc.region, err)
		}
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture-ambient-id")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture-ambient-material")
	_, err := New(core.Connection{ID: "x", Name: "name", Provider: core.AWS}, Credentials{})
	if err == nil || err.Error() != "error.cloudCredentials" {
		t.Fatal("accepted ambient credentials")
	}
}

func TestErrorRedactionAndRedirect(t *testing.T) {
	dest := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("followed redirect") }))
	defer dest.Close()
	client := wireClient(t, core.AWS, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("continuation-token") {
			http.Redirect(w, r, dest.URL, http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("x-amz-request-id", "safe-id")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, "<Error><Code>AccessDenied</Code><Message>private source context</Message></Error>")
	})
	_, err := client.ListBuckets(t.Context(), "")
	detail, ok := err.(*core.Error)
	if !ok || detail.Code != "error.cloudAccess" || detail.RequestID != "safe-id" || detail.ProviderCode != "AccessDenied" {
		t.Fatalf("wrong structured error %#v", err)
	}
	data, _ := json.Marshal(detail)
	if strings.Contains(string(data), "private source") || strings.Contains(string(data), "fixture-signing") {
		t.Fatal("error leakage")
	}
	if _, err := client.ListBuckets(t.Context(), "redirect"); err == nil {
		t.Fatal("redirect accepted")
	}
}
