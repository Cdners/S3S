package s3client

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"regexp"
	"strconv"

	"github.com/Cdners/S3S/internal/core"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	maxParts             = 10000
	maxPartBytes   int64 = 5 << 30
	taskMetadata         = "s3s-task"
	digestMetadata       = "s3s-sha256"
	emptySHA256          = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	emptyMD5Base64       = "1B2M2Y8AsgTpgAmY7PhCfg=="
)

var (
	taskMarker = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	hexDigest  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func validUpload(bucket, key, nonce, digest string) bool {
	return bucketName.MatchString(bucket) && key != "" && taskMarker.MatchString(nonce) && hexDigest.MatchString(digest)
}

func validSession(bucket, key, id string) bool {
	return bucketName.MatchString(bucket) && key != "" && id != ""
}

func uploadError(err error) *core.Error {
	e := cloudError(err)
	switch {
	case e.ProviderCode == "NoSuchUpload":
		e.Code = "error.uploadSessionMissing"
	case e.Code == "error.cloudChanged" || e.ProviderCode == "PreconditionFailed":
		e.Code = "error.uploadExists"
	case e.ProviderCode == "ConditionalRequestConflict":
		e.Code = core.Conflict
	}
	return e
}

func (c *Client) BeginUpload(ctx context.Context, bucket, key, nonce, sha256Hex string) (string, error) {
	if !validUpload(bucket, key, nonce, sha256Hex) {
		return "", core.NewError(core.InvalidTask)
	}
	out, err := c.api.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		Metadata: map[string]string{taskMetadata: nonce, digestMetadata: sha256Hex},
	})
	if err != nil {
		return "", uploadError(err)
	}
	if out.UploadId == nil || *out.UploadId == "" {
		return "", core.NewError("error.cloudRequest")
	}
	return *out.UploadId, nil
}

func (c *Client) maxPartSize() int64 {
	if c.provider == core.R2 {
		// R2's documented maximum for a single upload request is just below 5 GiB.
		return maxPartBytes - (5 << 20)
	}
	return maxPartBytes
}

// UploadPart reads exactly one seekable file section. Both providers validate
// Content-MD5 on receipt. The returned ETag is an opaque completion token.
func (c *Client) UploadPart(ctx context.Context, bucket, key, uploadID string, number int, body io.ReadSeeker, size int64, md5Base64 string) (string, error) {
	digest, err := base64.StdEncoding.Strict().DecodeString(md5Base64)
	if !validSession(bucket, key, uploadID) || number < 1 || number > maxParts || body == nil || size < 1 || size > c.maxPartSize() ||
		err != nil || len(digest) != 16 || base64.StdEncoding.EncodeToString(digest) != md5Base64 {
		return "", core.NewError(core.InvalidPart)
	}
	length, err := body.Seek(0, io.SeekEnd)
	if err != nil || length != size {
		return "", core.NewError(core.InvalidPart)
	}
	if _, err = body.Seek(0, io.SeekStart); err != nil {
		return "", core.NewError(core.InvalidPart)
	}
	out, err := c.api.UploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
		PartNumber: aws.Int32(int32(number)), Body: body, ContentLength: aws.Int64(size), ContentMD5: aws.String(md5Base64),
	}, func(o *s3.Options) { o.HTTPClient = c.partHTTP })
	if err != nil {
		return "", uploadError(err)
	}
	if !strongETag(aws.ToString(out.ETag)) {
		return "", core.NewError("error.cloudRequest")
	}
	return *out.ETag, nil
}

// ListParts fetches every page before exposing any result to recovery. Require
// increasing part numbers and numeric forward progress to avoid partial or
// cyclic reconciliation. S3 and R2 part numbers range from 1 through 10,000.
func (c *Client) ListParts(ctx context.Context, bucket, key, uploadID string) ([]core.Part, error) {
	if !validSession(bucket, key, uploadID) {
		return nil, core.NewError(core.InvalidTask)
	}
	in := &s3.ListPartsInput{Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID), MaxParts: aws.Int32(1000)}
	parts := []core.Part{}
	previous := 0
	for {
		out, err := c.api.ListParts(ctx, in)
		if err != nil {
			return nil, uploadError(err)
		}
		for _, p := range out.Parts {
			n := int(aws.ToInt32(p.PartNumber))
			if n <= previous || n > maxParts || p.Size == nil || *p.Size <= 0 || *p.Size > c.maxPartSize() || !strongETag(aws.ToString(p.ETag)) {
				return nil, core.NewError("error.cloudRequest")
			}
			parts = append(parts, core.Part{Number: n, Size: *p.Size, ETag: *p.ETag})
			previous = n
		}
		if !aws.ToBool(out.IsTruncated) {
			return parts, nil
		}
		marker, err := strconv.Atoi(aws.ToString(out.NextPartNumberMarker))
		if err != nil || len(out.Parts) == 0 || marker != previous || marker >= maxParts {
			return nil, core.NewError("error.cloudRequest")
		}
		in.PartNumberMarker = out.NextPartNumberMarker
	}
}

func (c *Client) CompleteUpload(ctx context.Context, bucket, key, uploadID string, parts []core.Part) error {
	if !validSession(bucket, key, uploadID) || len(parts) == 0 || len(parts) > maxParts {
		return core.NewError(core.InvalidTask)
	}
	completed := make([]types.CompletedPart, 0, len(parts))
	for i, p := range parts {
		if p.Number != i+1 || !strongETag(p.ETag) {
			return core.NewError(core.InvalidPart)
		}
		completed = append(completed, types.CompletedPart{PartNumber: aws.Int32(int32(p.Number)), ETag: aws.String(p.ETag)})
	}
	_, err := c.api.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
		IfNoneMatch: aws.String("*"), MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	})
	if err != nil {
		return uploadError(err)
	}
	return nil // The engine confirms publication with VerifyUpload before committing success.
}

func (c *Client) AbortUpload(ctx context.Context, bucket, key, uploadID string) error {
	if !validSession(bucket, key, uploadID) {
		return core.NewError(core.InvalidTask)
	}
	_, err := c.api.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
	})
	if err != nil {
		e := uploadError(err)
		if e.Code == "error.uploadSessionMissing" {
			return nil
		}
		return e
	}
	// The service may acknowledge abort while a canceled part request is
	// still draining. Keep the session available for a subsequent cleanup if
	// verification failed or the provider still reports stored parts.
	_, err = c.ListParts(ctx, bucket, key, uploadID)
	if err != nil {
		var e *core.Error
		if errors.As(err, &e) && e.Code == "error.uploadSessionMissing" {
			return nil
		}
		return err
	}
	// An empty but listable session could still accept workers in flight.
	e := core.NewError("error.uploadCleanup")
	e.Retryable = true
	return e
}

func (c *Client) PutEmpty(ctx context.Context, bucket, key, nonce, sha256Hex string) error {
	if !validUpload(bucket, key, nonce, sha256Hex) || sha256Hex != emptySHA256 {
		return core.NewError(core.InvalidTask)
	}
	_, err := c.api.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), IfNoneMatch: aws.String("*"),
		Body: bytes.NewReader(nil), ContentLength: aws.Int64(0), ContentMD5: aws.String(emptyMD5Base64),
		Metadata: map[string]string{taskMetadata: nonce, digestMetadata: sha256Hex},
	})
	if err != nil {
		return uploadError(err)
	}
	return nil
}

// VerifyUpload recognizes publication by this operation, including after a lost
// completion response. Size or ETag alone cannot distinguish another writer.
func (c *Client) VerifyUpload(ctx context.Context, bucket, key, nonce, sha256Hex string, size int64) (bool, error) {
	if !validUpload(bucket, key, nonce, sha256Hex) || size < 0 {
		return false, core.NewError(core.InvalidTask)
	}
	out, err := c.api.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		e := uploadError(err)
		if e.Code == "error.cloudMissing" {
			return false, nil
		}
		return false, e
	}
	if out.ContentLength == nil {
		return false, core.NewError("error.cloudRequest")
	}
	if *out.ContentLength != size || out.Metadata[taskMetadata] != nonce || out.Metadata[digestMetadata] != sha256Hex {
		return false, core.NewError("error.uploadExists")
	}
	return true, nil
}
