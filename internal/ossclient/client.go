// Package ossclient implements read-only Alibaba Cloud OSS operations using
// the native OSS V4 signer and the selected account's explicit credentials.
package ossclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Cdners/S3S/internal/core"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
)

type Client struct {
	api       *oss.Client
	host      string
	rangeHTTP *http.Client
}

var (
	regionName       = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)+$`)
	codeChars        = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	idChars          = regexp.MustCompile(`^[A-Za-z0-9._/+=-]{1,128}$`)
	defaultTransport = func() *http.Transport {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.ResponseHeaderTimeout = 3 * time.Minute
		return t
	}()
)

type expectedHostKey struct{}
type signedTransport struct{ base http.RoundTripper }

func (t signedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	expected, _ := r.Context().Value(expectedHostKey{}).(string)
	if expected == "" || r.URL.Scheme != "https" || r.URL.User != nil || r.URL.Host != expected || (r.Host != "" && r.Host != expected) ||
		(r.Method != http.MethodHead && r.Method != http.MethodGet) {
		return nil, errors.New("unexpected OSS request target")
	}
	return t.base.RoundTrip(r)
}

func New(c core.Connection, keys core.CloudCredentials, transport http.RoundTripper) (*Client, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Provider != core.OSS {
		return nil, core.NewError("error.cloudProvider")
	}
	region := c.Region
	var host string
	if c.Endpoint != "" {
		// Old profiles may have only a service endpoint. Parse the region
		// locally; keep the original SQLite/keyring scope exactly as stored.
		u, err := url.Parse(c.Endpoint)
		if err != nil || u.Scheme != "https" || strings.HasSuffix(u.Host, ":") || (u.Port() != "" && u.Port() != "443") {
			return nil, core.NewError(core.InvalidConnection)
		}
		host = strings.ToLower(u.Hostname())
		label, ok := strings.CutSuffix(host, ".aliyuncs.com")
		endpointRegion, regional := strings.CutPrefix(label, "oss-")
		endpointRegion = strings.TrimSuffix(endpointRegion, "-internal")
		if !ok || !regional || !regionName.MatchString(endpointRegion) || (region != "" && region != endpointRegion) {
			return nil, core.NewError(core.InvalidConnection)
		}
		region = endpointRegion
	} else {
		if !regionName.MatchString(region) {
			return nil, core.NewError(core.InvalidConnection)
		}
		host = "oss-" + region + ".aliyuncs.com"
	}
	if strings.TrimSpace(keys.AccessKeyID) == "" || strings.TrimSpace(keys.SecretAccessKey) == "" {
		return nil, core.NewError("error.cloudCredentials")
	}
	if transport == nil {
		transport = defaultTransport
	}
	httpClient := &http.Client{
		Transport: signedTransport{transport}, Timeout: 3 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	// NewConfig avoids environment-backed credentials and SDK debug log settings.
	cfg := oss.NewConfig().WithRegion(region).WithEndpoint("https://" + host).
		WithCredentialsProvider(credentials.NewStaticCredentialsProvider(keys.AccessKeyID, keys.SecretAccessKey, keys.SessionToken)).
		WithSignatureVersion(oss.SignatureVersionV4).WithHttpClient(httpClient).WithRetryMaxAttempts(1)
	rangeHTTP := *httpClient
	rangeHTTP.Timeout = 0 // A paced range may take longer than a metadata request.
	return &Client{api: oss.NewClient(cfg), host: host, rangeHTTP: &rangeHTTP}, nil
}

func mapError(err error) *core.Error {
	e := core.NewError("error.cloudRequest")
	var se *oss.ServiceError
	if errors.As(err, &se) {
		if codeChars.MatchString(se.Code) {
			e.ProviderCode = se.Code
		}
		if idChars.MatchString(se.RequestID) {
			e.RequestID = se.RequestID
		}
		switch se.StatusCode {
		case 401, 403:
			e.Code = "error.cloudAccess"
		case 404:
			e.Code = "error.cloudMissing"
		case 412:
			e.Code = "error.cloudChanged"
		case 416:
			e.Code = "error.cloudRange"
		default:
			e.Retryable = se.StatusCode == 408 || se.StatusCode == 429 || se.StatusCode >= 500
		}
		switch se.Code {
		case "InvalidAccessKeyId", "SignatureDoesNotMatch", "SecurityTokenExpired", "InvalidSecurityToken", "InvalidSecurityToken.Expired":
			e.Code = "error.cloudCredentials"
		}
	} else {
		var n net.Error
		e.Retryable = errors.As(err, &n) && !errors.Is(err, context.Canceled)
	}
	return e
}

func (c *Client) bucketContext(ctx context.Context, bucket string) (context.Context, error) {
	if !oss.IsValidBucketName(&bucket) {
		return nil, core.NewError(core.InvalidTask)
	}
	return context.WithValue(ctx, expectedHostKey{}, bucket+"."+c.host), nil
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func next(truncated bool, token *string, previous string) (string, error) {
	if !truncated {
		return "", nil
	}
	if token == nil || *token == "" || *token == previous {
		return "", core.NewError("error.cloudRequest")
	}
	return *token, nil
}

func (c *Client) ListBuckets(ctx context.Context, token string) (core.BucketPage, error) {
	ctx = context.WithValue(ctx, expectedHostKey{}, c.host)
	out, err := c.api.ListBuckets(ctx, &oss.ListBucketsRequest{Marker: optional(token), MaxKeys: 100})
	if err != nil {
		return core.BucketPage{}, mapError(err)
	}
	cursor, err := next(out.IsTruncated, out.NextMarker, token)
	if err != nil {
		return core.BucketPage{}, err
	}
	page := core.BucketPage{Buckets: make([]string, 0, len(out.Buckets)), NextToken: cursor}
	for _, b := range out.Buckets {
		if !oss.IsValidBucketName(b.Name) {
			return core.BucketPage{}, core.NewError("error.cloudRequest")
		}
		page.Buckets = append(page.Buckets, *b.Name)
	}
	return page, nil
}

func (c *Client) ListObjects(ctx context.Context, bucket, prefix, token string) (core.ObjectPage, error) {
	ctx, err := c.bucketContext(ctx, bucket)
	if err != nil {
		return core.ObjectPage{}, err
	}
	out, err := c.api.ListObjectsV2(ctx, &oss.ListObjectsV2Request{
		Bucket: &bucket, Prefix: &prefix, Delimiter: oss.Ptr("/"),
		MaxKeys: 100, ContinuationToken: optional(token),
	})
	if err != nil {
		return core.ObjectPage{}, mapError(err)
	}
	cursor, err := next(out.IsTruncated, out.NextContinuationToken, token)
	if err != nil {
		return core.ObjectPage{}, err
	}
	page := core.ObjectPage{Objects: make([]core.Object, 0, len(out.Contents)), Prefixes: make([]string, 0, len(out.CommonPrefixes)), NextToken: cursor}
	// The native SDK requests encoding-type=url and decodes the response once.
	for _, o := range out.Contents {
		if o.Key == nil || *o.Key == "" || o.Size < 0 {
			return core.ObjectPage{}, core.NewError("error.cloudRequest")
		}
		page.Objects = append(page.Objects, core.Object{Key: *o.Key, Size: o.Size, ETag: oss.ToString(o.ETag)})
	}
	for _, p := range out.CommonPrefixes {
		if p.Prefix == nil || *p.Prefix == "" {
			return core.ObjectPage{}, core.NewError("error.cloudRequest")
		}
		page.Prefixes = append(page.Prefixes, *p.Prefix)
	}
	return page, nil
}

func strongETag(s string) bool {
	return len(s) > 2 && s[0] == '"' && s[len(s)-1] == '"' && !strings.ContainsAny(s[1:len(s)-1], "\r\n\"")
}

func (c *Client) Head(ctx context.Context, bucket, key string) (core.ObjectIdentity, error) {
	ctx, err := c.bucketContext(ctx, bucket)
	if err != nil || key == "" {
		return core.ObjectIdentity{}, core.NewError(core.InvalidTask)
	}
	out, err := c.api.HeadObject(ctx, &oss.HeadObjectRequest{Bucket: &bucket, Key: &key})
	if err != nil {
		return core.ObjectIdentity{}, mapError(err)
	}
	if out.StatusCode != http.StatusOK || out.Headers.Get("Content-Length") == "" || out.ContentLength < 0 || !strongETag(oss.ToString(out.ETag)) {
		return core.ObjectIdentity{}, core.NewError("error.cloudChanged")
	}
	return core.ObjectIdentity{Size: out.ContentLength, ETag: *out.ETag, VersionID: oss.ToString(out.VersionId)}, nil
}

func (c *Client) ReadRange(ctx context.Context, bucket, key string, id core.ObjectIdentity, offset, length int64) (io.ReadCloser, error) {
	ctx, err := c.bucketContext(ctx, bucket)
	if err != nil || key == "" {
		return nil, core.NewError(core.InvalidTask)
	}
	if !strongETag(id.ETag) || offset < 0 || length <= 0 || length > id.Size || offset > id.Size-length {
		return nil, core.NewError("error.cloudRange")
	}
	last := offset + length - 1
	out, err := c.api.GetObject(ctx, &oss.GetObjectRequest{
		Bucket: &bucket, Key: &key, IfMatch: &id.ETag, VersionId: optional(id.VersionID),
		Range: oss.Ptr(fmt.Sprintf("bytes=%d-%d", offset, last)), RangeBehavior: oss.Ptr("standard"),
	}, func(o *oss.Options) { o.HttpClient = c.rangeHTTP })
	if err != nil {
		return nil, mapError(err)
	}
	if oss.ToString(out.ETag) != id.ETag || oss.ToString(out.VersionId) != id.VersionID {
		out.Body.Close()
		return nil, core.NewError("error.cloudChanged")
	}
	if out.StatusCode != http.StatusPartialContent || out.ContentLength != length ||
		oss.ToString(out.ContentRange) != fmt.Sprintf("bytes %d-%d/%d", offset, last, id.Size) {
		out.Body.Close()
		return nil, core.NewError("error.cloudRange")
	}
	return out.Body, nil
}
