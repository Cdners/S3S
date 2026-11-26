package s3client

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Cdners/S3S/internal/core"
)

func genericTLS(t *testing.T, region string, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	c, err := NewWithTransport(core.Connection{ID: "test", Name: "Test", Provider: core.S3Compatible, Region: region, Endpoint: server.URL + "/"},
		Credentials{AccessKeyID: "fixture-id", SecretAccessKey: "fixture-signing-material", SessionToken: "fixture-session"}, server.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Recompute SigV4 from what the TLS server actually received, including the
// explicit port in the Host header and S3's non-normalised escaped object path.
func verifyGenericSignature(t *testing.T, r *http.Request, region string) {
	t.Helper()
	authorization := r.Header.Get("Authorization")
	timestamp := r.Header.Get("X-Amz-Date")
	if len(timestamp) != 16 {
		t.Error("missing signing timestamp")
		return
	}
	date := timestamp[:8]
	scope := date + "/" + region + "/s3/aws4_request"
	tail, ok := strings.CutPrefix(authorization, "AWS4-HMAC-SHA256 Credential=fixture-id/"+scope+", SignedHeaders=")
	names, signature, found := strings.Cut(tail, ", Signature=")
	if !ok || !found || !strings.Contains(r.Host, ":") || r.Header.Get("X-Amz-Security-Token") != "fixture-session" {
		t.Error("invalid signing identity, scope, host or token")
		return
	}
	var headerLines strings.Builder
	for _, name := range strings.Split(names, ";") {
		value := strings.Join(r.Header.Values(name), ",")
		switch name {
		case "host":
			value = r.Host
		case "content-length":
			value = strconv.FormatInt(r.ContentLength, 10)
		}
		fmt.Fprintf(&headerLines, "%s:%s\n", name, value)
	}
	canonical := strings.Join([]string{r.Method, r.URL.EscapedPath(), strings.ReplaceAll(r.URL.Query().Encode(), "+", "%20"),
		headerLines.String(), names, r.Header.Get("X-Amz-Content-Sha256")}, "\n")
	digest := sha256.Sum256([]byte(canonical))
	sign := func(key []byte, text string) []byte {
		h := hmac.New(sha256.New, key)
		io.WriteString(h, text)
		return h.Sum(nil)
	}
	key := []byte("AWS4" + "fixture-signing-material")
	for _, element := range []string{date, region, "s3", "aws4_request"} {
		key = sign(key, element)
	}
	expected := sign(key, strings.Join([]string{"AWS4-HMAC-SHA256", timestamp, scope, hex.EncodeToString(digest[:])}, "\n"))
	if signature != hex.EncodeToString(expected) {
		t.Errorf("signature mismatch for %s", r.Method)
	}
}

func TestGenericSignedPathStyleRead(t *testing.T) {
	const key = "资料//../a +/%2F"
	c := genericTLS(t, "zone-3", func(w http.ResponseWriter, r *http.Request) {
		verifyGenericSignature(t, r, "zone-3")
		if r.URL.Query().Get("list-type") == "2" {
			if r.URL.Path != "/photos" || r.URL.Query().Get("prefix") != "资料//" || r.URL.Query().Get("continuation-token") != "opaque +/" {
				t.Error("incorrect listing scope")
			}
			fmt.Fprintf(w, `<ListBucketResult><EncodingType>url</EncodingType><IsTruncated>false</IsTruncated><Contents><Key>%s</Key><Size>5</Size></Contents></ListBucketResult>`, url.PathEscape(key))
			return
		}
		if r.URL.Path != "/photos/"+key {
			t.Errorf("unexpected object path: %q", r.URL.Path)
		}
		w.Header().Set("ETag", `"unchanged"`)
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "5")
			return
		}
		if r.Header.Get("Range") != "bytes=1-3" || r.Header.Get("If-Match") != `"unchanged"` {
			t.Error("missing download precondition")
		}
		w.Header().Set("Content-Range", "bytes 1-3/5")
		w.Header().Set("Content-Length", "3")
		w.WriteHeader(http.StatusPartialContent)
		io.WriteString(w, "bcd")
	})
	ctx := t.Context()
	page, err := c.ListObjects(ctx, "photos", "资料//", "opaque +/")
	if err != nil || len(page.Objects) != 1 || page.Objects[0].Key != key {
		t.Fatalf("list: %v", err)
	}
	id, err := c.Head(ctx, "photos", key)
	if err != nil {
		t.Fatal(err)
	}
	body, err := c.ReadRange(ctx, "photos", key, id, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil || string(data) != "bcd" {
		t.Fatalf("range: %v %q", err, data)
	}
}

func TestGenericBasicAndNativeBucketPagination(t *testing.T) {
	for _, native := range []bool{true, false} {
		c := genericTLS(t, "", func(w http.ResponseWriter, r *http.Request) {
			verifyGenericSignature(t, r, "us-east-1")
			io.WriteString(w, "<ListAllMyBucketsResult><Buckets>")
			switch {
			case native && r.URL.Query().Get("continuation-token") == "server +/":
				io.WriteString(w, "<Bucket><Name>last</Name></Bucket></Buckets>")
			case native:
				io.WriteString(w, "<Bucket><Name>first</Name></Bucket></Buckets><ContinuationToken>server +/</ContinuationToken>")
			default:
				if r.URL.Query().Has("continuation-token") {
					t.Error("sent lexical cursor to basic endpoint")
				}
				for i := 100; i >= 0; i-- {
					fmt.Fprintf(w, "<Bucket><Name>b%03d</Name></Bucket>", i)
				}
				io.WriteString(w, "</Buckets>")
			}
			io.WriteString(w, "</ListAllMyBucketsResult>")
		})
		page, err := c.ListBuckets(t.Context(), "")
		if err != nil || page.NextToken == "" {
			t.Fatalf("buckets: %v", err)
		}
		last, err := c.ListBuckets(t.Context(), page.NextToken)
		if err != nil || len(last.Buckets) != 1 || last.NextToken != "" {
			t.Fatalf("last page %#v: %v", last, err)
		}
		want := "b100"
		if native {
			want = "last"
		}
		if last.Buckets[0] != want {
			t.Fatal("page cursor lost")
		}
	}
}

func TestGenericRequiresHTTPSOriginAndSafeSigningRegion(t *testing.T) {
	for _, tc := range []struct {
		endpoint, region string
		ok               bool
	}{
		{"", "", false}, {"http://minio.example:9000", "", false}, {"https://example:0", "", false}, {"https://example:70000", "", false}, {"https://example:", "", false},
		{"https://name@example", "", false}, {"https://example/api", "", false}, {"https://example/?token=1", "", false}, {"https://example/#fragment", "", false},
		{"https://store.example:9443", "bad/region", false}, {"https://[::1]:9443/", "custom.region-01", true}, {"https://minio.internal", "", true},
	} {
		_, err := New(core.Connection{ID: "id", Name: "test", Provider: core.S3Compatible, Endpoint: tc.endpoint, Region: tc.region}, Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture"})
		if (err == nil) != tc.ok {
			t.Errorf("%q/%q valid=%t: %v", tc.endpoint, tc.region, tc.ok, err)
		}
	}
}
func TestGenericErrorsRedirectsAndWriteRestriction(t *testing.T) {
	dest := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("redirect reached a different endpoint") }))
	defer dest.Close()
	c := genericTLS(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("%s reached read-only endpoint", r.Method)
		}
		if r.URL.Query().Get("prefix") == "redirect" {
			http.Redirect(w, r, dest.URL, http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("X-Amz-Request-Id", "req123")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, "<Error><Code>AccessDenied</Code><Message>private server detail</Message></Error>")
	})
	_, err := c.ListObjects(t.Context(), "scoped-bucket", "", "")
	var detail *core.Error
	if !errors.As(err, &detail) || detail.Code != "error.cloudAccess" || detail.ProviderCode != "AccessDenied" || detail.RequestID != "req123" {
		t.Fatalf("scoped auth failure: %v", err)
	}
	b, _ := json.Marshal(detail)
	if strings.Contains(string(b), "private server detail") {
		t.Fatal("exposed response body")
	}
	if _, err = c.ListObjects(t.Context(), "scoped-bucket", "redirect", ""); err == nil {
		t.Fatal("followed redirect")
	}
	if _, err = c.BeginUpload(t.Context(), "scoped-bucket", "key", "nonce", emptySHA256); !errors.As(err, &detail) || detail.Code != "error.cloudProvider" {
		t.Fatalf("begin upload: %v", err)
	}
	if err = c.PutEmpty(t.Context(), "scoped-bucket", "key", "nonce", emptySHA256); !errors.As(err, &detail) || detail.Code != "error.cloudProvider" {
		t.Fatalf("put empty: %v", err)
	}
}

func TestCanonicalSigningOrigin(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "example.com" || r.TLS.ServerName != "example.com" {
			t.Errorf("Host/SNI mismatch: %q, %q", r.Host, r.TLS.ServerName)
		}
		io.WriteString(w, "<ListAllMyBucketsResult><Buckets/></ListAllMyBucketsResult>")
	}))
	defer server.Close()
	tlsTransport := server.Client().Transport.(*http.Transport).Clone()
	defer tlsTransport.CloseIdleConnections()
	tlsTransport.DialContext = func(ctx context.Context, netw, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, netw, server.Listener.Addr().String())
	}
	c, err := NewWithTransport(core.Connection{ID: "test", Name: "test", Provider: core.S3Compatible, Endpoint: "HTTPS://EXAMPLE.COM:443/"},
		Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture"}, tlsTransport)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ListBuckets(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
}
