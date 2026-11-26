package s3client

import (
	"context"
	"net/url"
	"sort"
	"strings"

	"github.com/Cdners/S3S/internal/core"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const pageSize = 100

type BucketPage = core.BucketPage
type Object = core.Object
type ObjectPage = core.ObjectPage

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (c *Client) ListBuckets(ctx context.Context, token string) (BucketPage, error) {
	in := &s3.ListBucketsInput{}
	// Older S3 services return a complete bucket list. An implementation
	// that returns a continuation token gets it back verbatim on the next call.
	// A colon cannot occur in a bucket name, keeping local cursors distinct.
	const remotePrefix = "cursor:"
	remote, isRemote := strings.CutPrefix(token, remotePrefix)
	if c.provider == core.S3Compatible && isRemote {
		in.ContinuationToken = aws.String(remote)
	} else if c.provider == core.AWS {
		in.MaxBuckets = aws.Int32(pageSize)
		in.ContinuationToken = optional(token)
	}
	out, err := c.api.ListBuckets(ctx, in)
	if err != nil {
		return BucketPage{}, cloudError(err)
	}
	page := BucketPage{Buckets: make([]string, 0, len(out.Buckets)), NextToken: aws.ToString(out.ContinuationToken)}
	for _, b := range out.Buckets {
		if b.Name == nil {
			return BucketPage{}, core.NewError("error.cloudRequest")
		}
		page.Buckets = append(page.Buckets, *b.Name)
	}
	if c.provider == core.S3Compatible && (page.NextToken != "" || isRemote) {
		if page.NextToken != "" {
			page.NextToken = remotePrefix + page.NextToken
		}
	} else if c.provider == core.R2 || c.provider == core.S3Compatible {
		// Page a complete ListBuckets response using a lexical
		// cursor, so inserts or deletes before that cursor don't shift the next page.
		if page.NextToken != "" {
			return BucketPage{}, core.NewError("error.cloudRequest")
		}
		sort.Strings(page.Buckets)
		start := sort.Search(len(page.Buckets), func(i int) bool { return page.Buckets[i] > token })
		page.Buckets = page.Buckets[start:]
		if len(page.Buckets) > pageSize {
			page.Buckets = page.Buckets[:pageSize]
			page.NextToken = page.Buckets[pageSize-1]
		}
	}
	if token != "" && token == page.NextToken {
		return BucketPage{}, core.NewError("error.cloudRequest")
	}
	return page, nil
}

func (c *Client) ListObjects(ctx context.Context, bucket, prefix, token string) (ObjectPage, error) {
	if !bucketName.MatchString(bucket) {
		return ObjectPage{}, core.NewError(core.InvalidTask)
	}
	out, err := c.api.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket), Prefix: aws.String(prefix), Delimiter: aws.String("/"), MaxKeys: aws.Int32(pageSize),
		ContinuationToken: optional(token), EncodingType: types.EncodingTypeUrl,
	})
	if err != nil {
		return ObjectPage{}, cloudError(err)
	}
	page := ObjectPage{Objects: make([]Object, 0, len(out.Contents)), Prefixes: make([]string, 0, len(out.CommonPrefixes)), NextToken: aws.ToString(out.NextContinuationToken)}
	if aws.ToBool(out.IsTruncated) && page.NextToken == "" || token != "" && page.NextToken == token {
		return ObjectPage{}, core.NewError("error.cloudRequest")
	}
	decode := func(p *string) (string, error) {
		if p == nil {
			return "", core.NewError("error.cloudRequest")
		}
		if out.EncodingType == types.EncodingTypeUrl {
			s, err := url.PathUnescape(*p)
			if err != nil {
				return "", core.NewError("error.cloudRequest")
			}
			return s, nil
		}
		return *p, nil
	}
	for _, o := range out.Contents {
		key, err := decode(o.Key)
		if err != nil {
			return ObjectPage{}, err
		}
		if o.Size == nil || *o.Size < 0 {
			return ObjectPage{}, core.NewError("error.cloudRequest")
		}
		page.Objects = append(page.Objects, Object{Key: key, Size: *o.Size, ETag: aws.ToString(o.ETag)})
	}
	for _, p := range out.CommonPrefixes {
		prefix, err := decode(p.Prefix)
		if err != nil {
			return ObjectPage{}, err
		}
		page.Prefixes = append(page.Prefixes, prefix)
	}
	return page, nil
}
