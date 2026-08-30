// Package objectstore is a thin S3 client for the agent's off-host backups.
// It targets any S3-compatible endpoint — MinIO in local development, Wasabi
// in production — through the same code path; only the endpoint and
// credentials differ.
package objectstore

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Config is read from the environment by ConfigFromEnv. A node that carries no
// backup credentials leaves Bucket/keys empty and Enabled() is false, in which
// case the backup manager refuses backup commands rather than pretending.
type Config struct {
	Endpoint        string // e.g. http://minio:9000 or https://s3.eu-central-2.wasabisys.com
	Region          string
	Bucket          string
	Prefix          string
	AccessKeyID     string
	SecretAccessKey string
	ForcePathStyle  bool // MinIO needs path-style; Wasabi accepts virtual-hosted
}

func (c Config) Enabled() bool {
	return c.Bucket != "" && c.AccessKeyID != "" && c.SecretAccessKey != ""
}

// ConfigFromEnv reads NUBIT_BACKUP_S3_* from the environment.
//
//	NUBIT_BACKUP_S3_ENDPOINT           http://minio:9000 (dev) / https://s3.<region>.wasabisys.com (prod)
//	NUBIT_BACKUP_S3_REGION             us-east-1 by default
//	NUBIT_BACKUP_S3_BUCKET             required to enable backups
//	NUBIT_BACKUP_S3_PREFIX             optional key prefix, e.g. "sites"
//	NUBIT_BACKUP_S3_ACCESS_KEY_ID      required
//	NUBIT_BACKUP_S3_SECRET_ACCESS_KEY  required
//	NUBIT_BACKUP_S3_FORCE_PATH_STYLE   "1" for MinIO; unset/"0" for Wasabi
func ConfigFromEnv() Config {
	return Config{
		Endpoint:        os.Getenv("NUBIT_BACKUP_S3_ENDPOINT"),
		Region:          os.Getenv("NUBIT_BACKUP_S3_REGION"),
		Bucket:          os.Getenv("NUBIT_BACKUP_S3_BUCKET"),
		Prefix:          os.Getenv("NUBIT_BACKUP_S3_PREFIX"),
		AccessKeyID:     os.Getenv("NUBIT_BACKUP_S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("NUBIT_BACKUP_S3_SECRET_ACCESS_KEY"),
		ForcePathStyle:  os.Getenv("NUBIT_BACKUP_S3_FORCE_PATH_STYLE") == "1",
	}
}

// Object is one stored archive.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// Store talks to one bucket. All keys are taken relative to the configured
// prefix, so callers pass "<siteID>/<name>" and never see the prefix.
type Store struct {
	client *s3.Client
	bucket string
	prefix string
}

// New builds a Store, or (nil, nil) when the config carries no credentials.
func New(ctx context.Context, c Config) (*Store, error) {
	if !c.Enabled() {
		return nil, nil
	}
	region := c.Region
	if region == "" {
		region = "us-east-1"
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(
		ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(c.AccessKeyID, c.SecretAccessKey, ""),
		),
	)
	if err != nil {
		return nil, err
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(c.Endpoint)
		}
		o.UsePathStyle = c.ForcePathStyle
	})

	return &Store{client: client, bucket: c.Bucket, prefix: strings.Trim(c.Prefix, "/")}, nil
}

func (s *Store) key(k string) string {
	if s.prefix == "" {
		return k
	}
	return s.prefix + "/" + strings.TrimPrefix(k, "/")
}

// Put stores body at key. body should be an *os.File or another io.ReadSeeker
// so the SDK can sign it without buffering the whole archive in memory.
func (s *Store) Put(ctx context.Context, key string, body io.Reader) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key(key)),
		Body:   body,
	})
	return err
}

// Get opens key for reading. The caller closes it.
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key(key)),
	})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}

// List returns every object whose key starts with prefix (relative to the
// configured prefix), newest first.
func (s *Store) List(ctx context.Context, prefix string) ([]Object, error) {
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(s.key(prefix)),
	})

	var objects []Object
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Contents {
			key := aws.ToString(item.Key)
			if s.prefix != "" {
				key = strings.TrimPrefix(key, s.prefix+"/")
			}
			objects = append(objects, Object{
				Key:          key,
				Size:         aws.ToInt64(item.Size),
				LastModified: aws.ToTime(item.LastModified),
			})
		}
	}
	return objects, nil
}

// Delete removes key. Deleting a key that is already gone is not an error.
func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key(key)),
	})
	var notFound *types.NoSuchKey
	if errors.As(err, &notFound) {
		return nil
	}
	return err
}
