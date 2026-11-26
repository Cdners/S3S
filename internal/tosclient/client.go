// Package tosclient implements regional object reads through the native TOS Go SDK.
package tosclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Cdners/S3S/internal/core"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

type Client struct {
	api  *tos.ClientV2
	host string
}

const metadataTimeout = 3 * time.Minute

var nativeTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = metadataTimeout
	t.DisableCompression = true
	return t
}()

type targetHost struct{}
type signedTransport struct{ base http.RoundTripper }

func (t signedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	expected, _ := req.Context().Value(targetHost{}).(string)
	if (req.Method != http.MethodGet && req.Method != http.MethodHead) || expected == "" || req.URL.Scheme != "https" || req.URL.Host != expected || (req.Host != "" && req.Host != expected) || req.URL.User != nil {
		return nil, errors.New("unexpected TOS request origin")
	}
	return t.base.RoundTrip(req)
}

func New(c core.Connection, keys core.CloudCredentials, transport http.RoundTripper) (*Client, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Provider != core.TOS {
		return nil, core.NewError("error.cloudProvider")
	}
	if strings.TrimSpace(keys.AccessKeyID) == "" || strings.TrimSpace(keys.SecretAccessKey) == "" {
		return nil, core.NewError("error.cloudCredentials")
	}

	region := c.Region
	host := tos.SupportedRegion()[region]
	if c.Endpoint != "" {
		u, err := url.Parse(c.Endpoint)
		if err != nil || u.Scheme != "https" || strings.HasSuffix(u.Host, ":") || (u.Port() != "" && u.Port() != "443") {
			return nil, core.NewError(core.InvalidConnection)
		}
		// Normalize only the signing destination. The underlying stored profile
		// (including historical DNS case and explicit :443) remains the vault scope.
		host = strings.ToLower(u.Hostname())
		mapped := tos.SupportedEndpoint()[host]
		if mapped == "" || (region != "" && region != mapped) {
			return nil, core.NewError(core.InvalidConnection)
		}
		region = mapped
	}
	if host == "" {
		return nil, core.NewError(core.InvalidConnection)
	}
	if transport == nil {
		transport = nativeTransport
	}
	httpClient := http.Client{Transport: signedTransport{transport},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	creds := tos.NewStaticCredentials(keys.AccessKeyID, keys.SecretAccessKey)
	creds.WithSecurityToken(keys.SessionToken)
	api, err := tos.NewClientV2("https://"+host, tos.WithCredentials(creds), tos.WithRegion(region),
		tos.WithTransport(tos.NewDefaultTransportWithClient(httpClient)), tos.WithMaxRetryCount(0))
	if err != nil {
		return nil, core.NewError(core.InvalidConnection)
	}
	return &Client{api: api, host: host}, nil
}

func (c *Client) bucketContext(ctx context.Context, bucket string) (context.Context, error) {
	if tos.IsValidBucketName(bucket) != nil {
		return nil, core.NewError(core.InvalidTask)
	}
	return context.WithValue(ctx, targetHost{}, bucket+"."+c.host), nil
}

func (c *Client) ListBuckets(ctx context.Context, token string) (core.BucketPage, error) {
	ctx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()
	ctx = context.WithValue(ctx, targetHost{}, c.host)
	out, err := c.api.ListBuckets(ctx, &tos.ListBucketsInput{})
	if err != nil {
		return core.BucketPage{}, cloudError(err)
	}
	// TOS's ListBuckets response has no service continuation token.
	names := make([]string, 0, len(out.Buckets))
	for _, b := range out.Buckets {
		if tos.IsValidBucketName(b.Name) != nil {
			return core.BucketPage{}, core.NewError("error.cloudRequest")
		}
		names = append(names, b.Name)
	}
	sort.Strings(names)
	first := sort.Search(len(names), func(i int) bool { return names[i] > token })
	page := core.BucketPage{Buckets: names[first:]}
	if len(page.Buckets) > 100 {
		page.Buckets = page.Buckets[:100]
		page.NextToken = page.Buckets[99]
	}
	return page, nil
}

func (c *Client) ListObjects(ctx context.Context, bucket, prefix, token string) (core.ObjectPage, error) {
	ctx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()
	ctx, err := c.bucketContext(ctx, bucket)
	if err != nil {
		return core.ObjectPage{}, err
	}
	// Native JSON responses preserve characters that cannot appear directly in
	// XML. No URL-encoding mode or second decoding pass is needed for keys.
	out, err := c.api.ListObjectsType2(ctx, &tos.ListObjectsType2Input{
		Bucket: bucket, Prefix: prefix, Delimiter: "/", ContinuationToken: token, MaxKeys: 100, ListOnlyOnce: true,
	})
	if err != nil {
		return core.ObjectPage{}, cloudError(err)
	}
	page := core.ObjectPage{Objects: make([]core.Object, 0, len(out.Contents)), Prefixes: make([]string, 0, len(out.CommonPrefixes))}
	if out.IsTruncated {
		if out.NextContinuationToken == "" || out.NextContinuationToken == token {
			return core.ObjectPage{}, core.NewError("error.cloudRequest")
		}
		page.NextToken = out.NextContinuationToken
	}
	for _, object := range out.Contents {
		if object.Key == "" || object.Size < 0 {
			return core.ObjectPage{}, core.NewError("error.cloudRequest")
		}
		page.Objects = append(page.Objects, core.Object{Key: object.Key, Size: object.Size, ETag: object.ETag})
	}
	for _, p := range out.CommonPrefixes {
		page.Prefixes = append(page.Prefixes, p.Prefix)
	}
	return page, nil
}

func strongETag(s string) bool {
	return len(s) > 2 && s[0] == '"' && s[len(s)-1] == '"' && !strings.ContainsAny(s[1:len(s)-1], "\"\r\n")
}

func (c *Client) Head(ctx context.Context, bucket, key string) (core.ObjectIdentity, error) {
	ctx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()
	ctx, err := c.bucketContext(ctx, bucket)
	if err != nil || key == "" {
		return core.ObjectIdentity{}, core.NewError(core.InvalidTask)
	}
	out, err := c.api.HeadObjectV2(ctx, &tos.HeadObjectV2Input{Bucket: bucket, Key: key})
	if err != nil {
		return core.ObjectIdentity{}, cloudError(err)
	}
	size, err := strconv.ParseInt(out.Header.Get("Content-Length"), 10, 64)
	if err != nil || size < 0 || !strongETag(out.ETag) {
		return core.ObjectIdentity{}, core.NewError("error.cloudChanged")
	}
	return core.ObjectIdentity{Size: size, ETag: out.ETag, VersionID: out.VersionID}, nil
}

func (c *Client) ReadRange(ctx context.Context, bucket, key string, id core.ObjectIdentity, offset, length int64) (io.ReadCloser, error) {
	ctx, err := c.bucketContext(ctx, bucket)
	if err != nil || key == "" || !strongETag(id.ETag) || offset < 0 || length <= 0 || length > id.Size || offset > id.Size-length {
		return nil, core.NewError("error.cloudRange")
	}
	end := offset + length - 1
	out, err := c.api.GetObjectV2(ctx, &tos.GetObjectV2Input{Bucket: bucket, Key: key, VersionID: id.VersionID,
		IfMatch: id.ETag, Range: fmt.Sprintf("bytes=%d-%d", offset, end)})
	if err != nil {
		e := cloudError(err)
		if tos.StatusCode(err) == http.StatusOK {
			e.Code = "error.cloudRange"
		} // provider ignored Range
		return nil, e
	}
	if out.ETag != id.ETag || out.VersionID != id.VersionID {
		out.Content.Close()
		return nil, core.NewError("error.cloudChanged")
	}
	if out.StatusCode != http.StatusPartialContent || out.ContentRange != fmt.Sprintf("bytes %d-%d/%d", offset, end, id.Size) ||
		out.Header.Get("Content-Length") != strconv.FormatInt(length, 10) || out.ContentLength != length {
		out.Content.Close()
		return nil, core.NewError("error.cloudRange")
	}
	return out.Content, nil
}

var (
	providerCode = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	requestID    = regexp.MustCompile(`^[A-Za-z0-9._/+=-]{1,128}$`)
)

func cloudError(err error) *core.Error {
	e := core.NewError("error.cloudRequest")
	var server *tos.TosServerError
	if !errors.As(err, &server) {
		var client *tos.TosClientError
		var network net.Error
		if errors.As(err, &client) && errors.As(client.Cause, &network) && !errors.Is(client.Cause, context.Canceled) {
			e.Retryable = true
		}
		return e
	}
	code := server.Code
	if code == "" {
		code = server.RequestInfo.EcCode
	}
	if providerCode.MatchString(code) {
		e.ProviderCode = code
	}
	if requestID.MatchString(server.RequestID) {
		e.RequestID = server.RequestID
	}
	switch server.StatusCode {
	case 401, 403:
		e.Code = "error.cloudAccess"
	case 404:
		e.Code = "error.cloudMissing"
	case 412:
		e.Code = "error.cloudChanged"
	case 416:
		e.Code = "error.cloudRange"
	default:
		e.Retryable = server.StatusCode == 408 || server.StatusCode == 429 || server.StatusCode >= 500
	}
	switch e.ProviderCode {
	case "InvalidAccessKeyId", "SignatureDoesNotMatch", "ExpiredToken", "InvalidSecurityToken":
		e.Code = "error.cloudCredentials"
	}
	return e
}
