package s3client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Cdners/S3S/internal/core"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

func strongETag(s string) bool {
	return len(s) > 2 && s[0] == '"' && s[len(s)-1] == '"' && !strings.ContainsAny(s[1:len(s)-1], "\"\r\n")
}

func (c *Client) Head(ctx context.Context, bucket, key string) (core.ObjectIdentity, error) {
	if !bucketName.MatchString(bucket) || key == "" {
		return core.ObjectIdentity{}, core.NewError(core.InvalidTask)
	}
	out, err := c.api.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return core.ObjectIdentity{}, cloudError(err)
	}
	if out.ContentLength == nil || *out.ContentLength < 0 || !strongETag(aws.ToString(out.ETag)) {
		return core.ObjectIdentity{}, core.NewError("error.cloudChanged")
	}
	return core.ObjectIdentity{Size: *out.ContentLength, ETag: *out.ETag, VersionID: aws.ToString(out.VersionId)}, nil
}

func (c *Client) ReadRange(ctx context.Context, bucket, key string, id core.ObjectIdentity, offset, length int64) (io.ReadCloser, error) {
	if !bucketName.MatchString(bucket) || key == "" || !strongETag(id.ETag) || offset < 0 || length <= 0 || length > id.Size || offset > id.Size-length {
		return nil, core.NewError("error.cloudRange")
	}
	last := offset + length - 1
	out, err := c.api.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key),
		Range: aws.String(fmt.Sprintf("bytes=%d-%d", offset, last)), IfMatch: aws.String(id.ETag), VersionId: optional(id.VersionID)})
	if err != nil {
		return nil, cloudError(err)
	}
	if aws.ToString(out.ETag) != id.ETag || aws.ToString(out.VersionId) != id.VersionID {
		out.Body.Close()
		return nil, core.NewError("error.cloudChanged")
	}
	raw, ok := awsmiddleware.GetRawResponse(out.ResultMetadata).(*smithyhttp.Response)
	expected := fmt.Sprintf("bytes %d-%d/%d", offset, last, id.Size)
	if !ok || raw.StatusCode != http.StatusPartialContent || aws.ToString(out.ContentRange) != expected || out.ContentLength == nil || *out.ContentLength != length {
		out.Body.Close()
		return nil, core.NewError("error.cloudRange")
	}
	return out.Body, nil // Caller confirms the byte count and owns Close.
}
