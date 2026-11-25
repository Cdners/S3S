// Package s3client implements cloud operations using a single connection's
// explicit credentials. It never consults AWS profiles, IMDS, or credential env vars.
package s3client

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Cdners/S3S/internal/core"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type Credentials struct {
	AccessKeyID     string `json:"-"`
	SecretAccessKey string `json:"-"`
	SessionToken    string `json:"-"`
}

type Client struct {
	api      *s3.Client
	partHTTP *http.Client
	provider core.Provider
}

var (
	// Bound the wait after a request body has finished sending. A healthy
	// multi-GiB part on a slow connection can legitimately take hours to send.
	storageTransport = func() *http.Transport {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.ResponseHeaderTimeout = 3 * time.Minute
		return t
	}()
	regionName   = regexp.MustCompile(`^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$`)
	r2Host       = regexp.MustCompile(`^[a-f0-9]{32}(?:\.(?:eu|fedramp))?\.r2\.cloudflarestorage\.com$`)
	bucketName   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,253}$`)
	providerCode = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,128}$`)
	requestID    = regexp.MustCompile(`^[a-zA-Z0-9._/+=-]{1,128}$`)
)

func New(c core.Connection, creds Credentials) (*Client, error) {
	return NewWithTransport(c, creds, nil)
}

// NewWithTransport applies the same endpoint and credential checks as New.
// A nil transport uses the shared storage transport. An explicit transport
// runs beneath the origin guard for every request from this client.
func NewWithTransport(c core.Connection, creds Credentials, transport http.RoundTripper) (*Client, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Provider != core.AWS && c.Provider != core.R2 {
		return nil, core.NewError("error.cloudProvider")
	}
	if strings.TrimSpace(creds.AccessKeyID) == "" || strings.TrimSpace(creds.SecretAccessKey) == "" {
		return nil, core.NewError("error.cloudCredentials")
	}

	region := c.Region
	endpoint := strings.TrimSuffix(c.Endpoint, "/")
	if c.Provider == core.AWS {
		if region == "" {
			region = "us-east-1"
		}
		if !regionName.MatchString(region) {
			return nil, core.NewError(core.InvalidConnection)
		}
		suffix := ".amazonaws.com"
		if strings.HasPrefix(region, "cn-") {
			suffix += ".cn"
		}
		regional := "https://s3." + region + suffix
		if endpoint == "" {
			endpoint = regional
		}
		if endpoint != regional && !(region == "us-east-1" && endpoint == "https://s3.amazonaws.com") {
			return nil, core.NewError(core.InvalidConnection)
		}
	} else {
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" || !r2Host.MatchString(u.Host) {
			return nil, core.NewError(core.InvalidConnection)
		}
		region = "auto"
	}
	if transport == nil {
		transport = storageTransport
	}
	return newClient(c.Provider, region, endpoint, creds, transport), nil
}

// Reject a signed request if SDK endpoint resolution ever leaves this connection's
// origin (for example, an access point ARN or a redirect).
type originTransport struct {
	origin string
	base   http.RoundTripper
}

func (t originTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme+"://"+req.URL.Host != t.origin {
		return nil, errors.New("unexpected storage origin")
	}
	return t.base.RoundTrip(req)
}

func newClient(provider core.Provider, region, endpoint string, creds Credentials, transport http.RoundTripper) *Client {
	metadataHTTP := &http.Client{
		Transport:     originTransport{endpoint, transport},
		Timeout:       3 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	partHTTP := *metadataHTTP
	partHTTP.Timeout = 0 // Use task cancellation and the transport's response-header deadline.
	api := s3.New(s3.Options{
		Region: region, BaseEndpoint: aws.String(endpoint), UsePathStyle: true,
		Credentials:                credentials.NewStaticCredentialsProvider(creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken),
		HTTPClient:                 metadataHTTP,
		Retryer:                    aws.NopRetryer{},                           // the engine owns retry budgets and checkpoints
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, // uploads carry an explicit Content-MD5
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &Client{api: api, partHTTP: &partHTTP, provider: provider}
}

func cloudError(err error) *core.Error {
	result := core.NewError("error.cloudRequest")
	if errors.Is(err, context.Canceled) {
		return result
	}
	var api smithy.APIError
	if errors.As(err, &api) && providerCode.MatchString(api.ErrorCode()) {
		result.ProviderCode = api.ErrorCode()
	}
	var identifiable interface{ ServiceRequestID() string }
	if errors.As(err, &identifiable) && requestID.MatchString(identifiable.ServiceRequestID()) {
		result.RequestID = identifiable.ServiceRequestID()
	}
	var response interface{ HTTPStatusCode() int }
	if errors.As(err, &response) {
		switch status := response.HTTPStatusCode(); status {
		case 401, 403:
			result.Code = "error.cloudAccess"
		case 404:
			result.Code = "error.cloudMissing"
		case 412:
			result.Code = "error.cloudChanged"
		case 416:
			result.Code = "error.cloudRange"
		default:
			result.Retryable = status == 408 || status == 429 || status >= 500
		}
	} else {
		var network net.Error
		result.Retryable = errors.As(err, &network)
	}
	switch result.ProviderCode {
	case "InvalidAccessKeyId", "SignatureDoesNotMatch", "ExpiredToken", "InvalidToken":
		result.Code = "error.cloudCredentials"
	}
	return result
}
