package sync

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
)

// S3Config configures an S3-compatible backend. The same struct targets every
// provider; only the preset values differ:
//
//	AWS S3     Endpoint:""                                     Region:"us-east-1" PathStyle:false
//	Cloudflare R2 (default) Endpoint:"https://<acct>.r2.cloudflarestorage.com" Region:"auto" PathStyle:false
//	Backblaze B2  Endpoint:"https://s3.<region>.backblazeb2.com" Region:<region> PathStyle:false
//	MinIO (self-host) Endpoint:"http://host:9000"               Region:"us-east-1" PathStyle:true
type S3Config struct {
	Endpoint        string
	Region          string
	Bucket          string
	Prefix          string // e.g. "weft/v1"; bucket keys are <prefix>/<key>
	AccessKeyID     string
	SecretAccessKey string
	PathStyle       bool
}

// S3Backend implements Backend over any S3-compatible object store via
// aws-sdk-go-v2. It speaks only the six Backend verbs; all convergence + E2EE
// logic lives above it, so the store is dumb and sees only ciphertext.
type S3Backend struct {
	c      *s3.Client
	bucket string
	prefix string
}

// NewS3Backend builds the client. Static credentials are used (the scoped
// bucket key handed out by the deploy template); no shared config files.
func NewS3Backend(cfg S3Config) *S3Backend {
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	opts := s3.Options{
		Region:       region,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		UsePathStyle: cfg.PathStyle,
	}
	if cfg.Endpoint != "" {
		opts.BaseEndpoint = aws.String(cfg.Endpoint)
	}
	return &S3Backend{
		c:      s3.New(opts),
		bucket: cfg.Bucket,
		prefix: strings.Trim(cfg.Prefix, "/"),
	}
}

func (b *S3Backend) full(key string) string {
	if b.prefix == "" {
		return key
	}
	return b.prefix + "/" + key
}

func (b *S3Backend) strip(key string) string {
	if b.prefix == "" {
		return key
	}
	return strings.TrimPrefix(key, b.prefix+"/")
}

func isNotFound(err error) bool {
	var nsk *types.NoSuchKey
	var nf *types.NotFound
	if errors.As(err, &nsk) || errors.As(err, &nf) {
		return true
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return false
}

func (b *S3Backend) Get(key string) ([]byte, error) {
	out, err := b.c.GetObject(context.TODO(), &s3.GetObjectInput{
		Bucket: aws.String(b.bucket), Key: aws.String(b.full(key)),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func (b *S3Backend) Put(key string, data []byte) error {
	_, err := b.c.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: aws.String(b.bucket), Key: aws.String(b.full(key)),
		Body: bytes.NewReader(data),
	})
	return err
}

// PutIfAbsent is a Head-then-Put — used only to skip re-uploading immutable
// content-addressed blobs, never for correctness, so the benign Head/Put race
// (two devices writing the SAME bytes) is harmless and we avoid depending on
// If-None-Match, whose support is uneven across S3 clones.
func (b *S3Backend) PutIfAbsent(key string, data []byte) (bool, error) {
	if ok, err := b.Head(key); err != nil {
		return false, err
	} else if ok {
		return false, nil
	}
	return true, b.Put(key, data)
}

func (b *S3Backend) Head(key string) (bool, error) {
	_, err := b.c.HeadObject(context.TODO(), &s3.HeadObjectInput{
		Bucket: aws.String(b.bucket), Key: aws.String(b.full(key)),
	})
	if err == nil {
		return true, nil
	}
	if isNotFound(err) {
		return false, nil
	}
	return false, err
}

func (b *S3Backend) List(prefix string) ([]string, error) {
	p := s3.NewListObjectsV2Paginator(b.c, &s3.ListObjectsV2Input{
		Bucket: aws.String(b.bucket), Prefix: aws.String(b.full(prefix)),
	})
	var keys []string
	for p.HasMorePages() {
		page, err := p.NextPage(context.TODO())
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			if obj.Key != nil {
				keys = append(keys, b.strip(*obj.Key))
			}
		}
	}
	return keys, nil
}

func (b *S3Backend) Delete(key string) error {
	_, err := b.c.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
		Bucket: aws.String(b.bucket), Key: aws.String(b.full(key)),
	})
	if isNotFound(err) {
		return nil
	}
	return err
}
