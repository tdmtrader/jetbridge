package durable

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3 stores objects in an S3-compatible bucket.
//
// S3-compatible rather than S3: the endpoint is configurable and addressing
// falls back to path style, so the same backend serves MinIO, Ceph, R2 and
// Backblaze. That matters more than native support for any one cloud — a
// self-hosted JetBridge is far likelier to have a MinIO than an AWS account.
//
// aws-sdk-go-v2 is already a direct dependency of atc/creds/ssm and
// atc/creds/secretsmanager, so this adds one service package rather than a
// vendor tree.
type S3 struct {
	client *s3.Client
	bucket string
	prefix string
	limit  int64
}

var _ Store = (*S3)(nil)

// S3Config configures the S3-compatible backend.
type S3Config struct {
	// Bucket is required.
	Bucket string

	// Prefix namespaces this deployment's objects inside the bucket, so one
	// bucket can serve several clusters. Optional.
	Prefix string

	// Endpoint overrides the AWS endpoint. Set it for MinIO and friends;
	// leave empty for real S3.
	Endpoint string

	// Region is required by the SDK's signer even when Endpoint points
	// somewhere that ignores it. Defaults to us-east-1.
	Region string

	// AccessKeyID and SecretAccessKey set static credentials. Leave both
	// empty to use the SDK's default chain, which is what picks up IRSA and
	// Workload Identity — the right answer on a managed cluster, because
	// nothing then has to hold a long-lived key.
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string

	// Limit bounds a single object in bytes; zero or less is unbounded.
	Limit int64

	// MaxAttempts bounds how hard the client tries before giving up.
	//
	// It is configuration rather than a property of the backend because the
	// right answer depends entirely on whether the bytes are replaceable. For
	// the artifact daemon a miss is free, so retrying is latency a build pays
	// for an answer it will discard, and 2 is right. For a caller whose bytes
	// have no upstream — an agent's workspace, say — giving up after two
	// attempts on a transient 503 loses data, and it should ask for more.
	//
	// Zero means 2.
	MaxAttempts int
}

// NewS3 builds an S3-compatible store.
func NewS3(ctx context.Context, cfg S3Config) (*S3, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("durable: s3 bucket is required")
	}

	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}

	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
	}
	if cfg.AccessKeyID != "" || cfg.SecretAccessKey != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken,
			),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("durable: load aws config: %w", err)
	}

	attempts := cfg.MaxAttempts
	if attempts <= 0 {
		attempts = 2
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.RetryMaxAttempts = attempts

		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			// Virtual-host addressing needs per-bucket DNS, which a MinIO
			// running at a bare host:port does not have.
			o.UsePathStyle = true
		}
	})

	return &S3{
		client: client,
		bucket: cfg.Bucket,
		prefix: strings.Trim(cfg.Prefix, "/"),
		limit:  cfg.Limit,
	}, nil
}

func (s *S3) objectKey(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	if s.prefix == "" {
		return key, nil
	}

	return s.prefix + "/" + key, nil
}

// isMiss reports whether err means "no such object" rather than a real
// failure. The SDK models it as a typed NoSuchKey on GetObject but as a bare
// 404 NotFound on HeadObject, so both have to be recognised.
func isMiss(err error) bool {
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return true
	}

	return false
}

// Stat issues a HEAD so a cache probe does not pay for the body.
func (s *S3) Stat(ctx context.Context, key string) (Attributes, bool, error) {
	objKey, err := s.objectKey(key)
	if err != nil {
		return Attributes{}, false, err
	}

	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objKey),
	})
	switch {
	case err == nil:
		return Attributes{
			Key:     key,
			Size:    aws.ToInt64(out.ContentLength),
			Updated: aws.ToTime(out.LastModified),
			Version: aws.ToString(out.VersionId),
		}, true, nil
	case isMiss(err):
		return Attributes{}, false, nil
	default:
		return Attributes{}, false, fmt.Errorf("durable: head %s: %w", key, err)
	}
}

// Get opens the object body. A missing object is a miss, not an error.
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, bool, error) {
	objKey, err := s.objectKey(key)
	if err != nil {
		return nil, false, err
	}

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objKey),
	})
	switch {
	case err == nil:
		return out.Body, true, nil
	case isMiss(err):
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("durable: get %s: %w", key, err)
	}
}

// Put uploads the object.
//
// The body is buffered to a temporary file first because PutObject needs a
// seekable reader to sign and to retry, and the alternative — holding a
// multi-gigabyte resource cache in memory on every node — is how a DaemonSet
// OOMs a cluster.
func (s *S3) Put(ctx context.Context, key string, body io.Reader) error {
	objKey, err := s.objectKey(key)
	if err != nil {
		return err
	}

	spooled, size, err := spool(LimitReader(body, s.limit))
	if err != nil {
		return err
	}
	defer spooled.cleanup()

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(objKey),
		Body:          spooled.file,
		ContentLength: aws.Int64(size),
	})
	if err != nil {
		return fmt.Errorf("durable: put %s: %w", key, err)
	}

	return nil
}

// Delete removes the object. S3 reports deleting an absent key as success,
// which is the semantics this interface wants.
func (s *S3) Delete(ctx context.Context, key string) error {
	objKey, err := s.objectKey(key)
	if err != nil {
		return err
	}

	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objKey),
	})
	if err != nil && !isMiss(err) {
		return fmt.Errorf("durable: delete %s: %w", key, err)
	}

	return nil
}

// List pages through the bucket under this store's prefix.
//
// Keys that do not round-trip through objectKey are skipped: a bucket shared
// with another consumer — v4's snapshot store is the expected one — holds
// objects that are not this store's to enumerate, and a reclaim pass must not
// mistake them for orphans.
func (s *S3) List(ctx context.Context, fn func(Attributes) error) error {
	input := &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket)}
	if s.prefix != "" {
		input.Prefix = aws.String(s.prefix + "/")
	}

	pager := s3.NewListObjectsV2Paginator(s.client, input)
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("durable: list: %w", err)
		}

		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			if s.prefix != "" {
				key = strings.TrimPrefix(key, s.prefix+"/")
			}
			if ValidateKey(key) != nil {
				continue
			}

			if err := fn(Attributes{Key: key, Size: aws.ToInt64(obj.Size), Updated: aws.ToTime(obj.LastModified)}); err != nil {
				return err
			}
		}
	}

	return nil
}
