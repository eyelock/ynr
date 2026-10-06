package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// S3 is the first cloud adapter (ADR-005): a bucket and a prefix, which also covers
// S3-compatible stores such as MinIO and R2.
//
//	s3://bucket/prefix?region=eu-west-2[&endpoint=http://127.0.0.1:9000&path_style=true][&cache=/dir]
//
// Credentials come from the AWS SDK's usual chain: the environment, a profile, or the role of the
// machine or job. Objects DuckDB reads are fetched into a local cache first, so the full build
// needs no DuckDB extension to reach S3.
type S3 struct {
	client *s3.Client
	bucket string
	prefix string // "" or ending in "/"
	cache  string
	raw    string
}

func openS3(ctx context.Context, raw string, u *url.URL) (*S3, error) {
	if u.Host == "" {
		return nil, fmt.Errorf("store %q: name a bucket, as s3://bucket/prefix", raw)
	}
	q := u.Query()
	prefix := strings.Trim(u.Path, "/")
	if prefix != "" {
		if !ValidKey(prefix) {
			return nil, fmt.Errorf("store %q: invalid prefix %q", raw, prefix)
		}
		prefix += "/"
	}
	var opts []func(*config.LoadOptions) error
	if region := q.Get("region"); region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("store %q: %w", raw, err)
	}
	pathStyle, _ := strconv.ParseBool(q.Get("path_style"))
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if ep := q.Get("endpoint"); ep != "" {
			o.BaseEndpoint = aws.String(ep)
		}
		o.UsePathStyle = pathStyle
	})
	cache := q.Get("cache")
	if cache == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("store %q: no cache folder: %w", raw, err)
		}
		cache = filepath.Join(base, "ynr", "s3", u.Host, filepath.FromSlash(strings.TrimSuffix(prefix, "/")))
	}
	return &S3{client: client, bucket: u.Host, prefix: prefix, cache: cache, raw: raw}, nil
}

// URL returns the store's URL.
func (s *S3) URL() string { return s.raw }

func (s *S3) key(k string) (string, error) {
	if !ValidKey(k) {
		return "", fmt.Errorf("store: invalid key %q", k)
	}
	return s.prefix + k, nil
}

// Put writes an object only if none is there yet, with a conditional write, so a batch can
// never be replaced.
func (s *S3) Put(ctx context.Context, key string, data []byte) error {
	k, err := s.key(key)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.bucket, Key: &k, Body: bytes.NewReader(data), IfNoneMatch: aws.String("*"),
	})
	if status(err) == http.StatusPreconditionFailed || status(err) == http.StatusConflict {
		return errors.Join(os.ErrExist, fmt.Errorf("store: %s already exists", key))
	}
	return err
}

// Replace writes an object whether or not one is there.
func (s *S3) Replace(ctx context.Context, key string, data []byte) error {
	k, err := s.key(key)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: &k, Body: bytes.NewReader(data)})
	return err
}

// Get reads an object.
func (s *S3) Get(ctx context.Context, key string) ([]byte, error) {
	k, err := s.key(key)
	if err != nil {
		return nil, err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &k})
	if notFound(err) {
		return nil, fmt.Errorf("store: %s: %w", key, os.ErrNotExist)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = out.Body.Close() }()
	return io.ReadAll(out.Body)
}

// Delete removes an object; S3 treats one already gone as deleted.
func (s *S3) Delete(ctx context.Context, key string) error {
	k, err := s.key(key)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &k})
	if notFound(err) {
		return nil
	}
	return err
}

// List returns the keys under a prefix, in key order, skipping any not of a valid shape.
func (s *S3) List(ctx context.Context, prefix string) ([]string, error) {
	sizes, err := s.Sizes(ctx, prefix)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(sizes))
	for k := range sizes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// Sizes lists the keys under a prefix with their sizes.
func (s *S3) Sizes(ctx context.Context, prefix string) (map[string]int64, error) {
	if prefix != "" && (!strings.HasSuffix(prefix, "/") || !ValidKey(strings.TrimSuffix(prefix, "/"))) {
		return nil, fmt.Errorf("store: invalid prefix %q", prefix)
	}
	full := s.prefix + prefix
	out := map[string]int64{}
	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: &full})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			k := strings.TrimPrefix(aws.ToString(o.Key), s.prefix)
			if ValidKey(k) {
				out[k] = aws.ToInt64(o.Size)
			}
		}
	}
	return out, nil
}

// Local fetches an object into the cache and returns its path there. Batches and compacted
// parts never change once written, so a cached copy is used as it is; the item index and
// rollups are replaced as they are rebuilt, so they are fetched afresh every time.
func (s *S3) Local(ctx context.Context, key string) (string, error) {
	if _, err := s.key(key); err != nil {
		return "", err
	}
	dst := filepath.Join(s.cache, filepath.FromSlash(key))
	if immutable(key) {
		if _, err := os.Stat(dst); err == nil {
			return dst, nil
		}
	}
	data, err := s.Get(ctx, key)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+path.Base(key)+".*")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return dst, os.Rename(tmp.Name(), dst)
}

// immutable reports whether a key, once written, never changes.
func immutable(key string) bool {
	return !strings.HasPrefix(key, "index/") && !strings.HasPrefix(key, "rollups/")
}

func status(err error) int {
	var re interface{ HTTPStatusCode() int }
	if errors.As(err, &re) {
		return re.HTTPStatusCode()
	}
	return 0
}

func notFound(err error) bool {
	if err == nil {
		return false
	}
	var nsk *types.NoSuchKey
	var nf *types.NotFound
	var ae smithy.APIError
	return errors.As(err, &nsk) || errors.As(err, &nf) || (errors.As(err, &ae) && ae.ErrorCode() == "NoSuchKey") ||
		status(err) == http.StatusNotFound
}

// EnsureBucket creates the bucket if it does not exist, for tests against a local MinIO.
// Production buckets come from infra/aws.
func (s *S3) EnsureBucket(ctx context.Context) error {
	_, err := s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &s.bucket})
	var owned *types.BucketAlreadyOwnedByYou
	var exists *types.BucketAlreadyExists
	if err == nil || errors.As(err, &owned) || errors.As(err, &exists) {
		return nil
	}
	return err
}
