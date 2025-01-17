package s3

import (
	"context"
	"io"
	"time"

	"github.com/go-kit/log"
	"github.com/prometheus/common/model"
	"github.com/thanos-io/objstore"
	"github.com/thanos-io/objstore/providers/s3"

	"github.com/cortexproject/cortex/pkg/util/backoff"
)

var defaultOperationRetries = 5
var defaultRetryMinBackoff = 5 * time.Second
var defaultRetryMaxBackoff = 1 * time.Minute

// NewBucketClient creates a new S3 bucket client
func NewBucketClient(cfg Config, name string, logger log.Logger) (objstore.Bucket, error) {
	s3Cfg, err := newS3Config(cfg)
	if err != nil {
		return nil, err
	}

	bucket, err := s3.NewBucketWithConfig(logger, s3Cfg, name)
	if err != nil {
		return nil, err
	}
	return &BucketWithRetries{
		bucket:           bucket,
		operationRetries: defaultOperationRetries,
		retryMinBackoff:  defaultRetryMinBackoff,
		retryMaxBackoff:  defaultRetryMaxBackoff,
	}, nil
}

// NewBucketReaderClient creates a new S3 bucket client
func NewBucketReaderClient(cfg Config, name string, logger log.Logger) (objstore.BucketReader, error) {
	s3Cfg, err := newS3Config(cfg)
	if err != nil {
		return nil, err
	}

	bucket, err := s3.NewBucketWithConfig(logger, s3Cfg, name)
	if err != nil {
		return nil, err
	}
	return &BucketWithRetries{
		bucket:           bucket,
		operationRetries: defaultOperationRetries,
		retryMinBackoff:  defaultRetryMinBackoff,
		retryMaxBackoff:  defaultRetryMaxBackoff,
	}, nil
}

func newS3Config(cfg Config) (s3.Config, error) {
	sseCfg, err := cfg.SSE.BuildThanosConfig()
	if err != nil {
		return s3.Config{}, err
	}
	bucketLookupType, err := cfg.bucketLookupType()
	if err != nil {
		return s3.Config{}, err
	}

	return s3.Config{
		Bucket:    cfg.BucketName,
		Endpoint:  cfg.Endpoint,
		Region:    cfg.Region,
		AccessKey: cfg.AccessKeyID,
		SecretKey: cfg.SecretAccessKey.Value,
		Insecure:  cfg.Insecure,
		SSEConfig: sseCfg,
		HTTPConfig: s3.HTTPConfig{
			IdleConnTimeout:       model.Duration(cfg.HTTP.IdleConnTimeout),
			ResponseHeaderTimeout: model.Duration(cfg.HTTP.ResponseHeaderTimeout),
			InsecureSkipVerify:    cfg.HTTP.InsecureSkipVerify,
			TLSHandshakeTimeout:   model.Duration(cfg.HTTP.TLSHandshakeTimeout),
			ExpectContinueTimeout: model.Duration(cfg.HTTP.ExpectContinueTimeout),
			MaxIdleConns:          cfg.HTTP.MaxIdleConns,
			MaxIdleConnsPerHost:   cfg.HTTP.MaxIdleConnsPerHost,
			MaxConnsPerHost:       cfg.HTTP.MaxConnsPerHost,
			Transport:             cfg.HTTP.Transport,
		},
		// Enforce signature version 2 if CLI flag is set
		SignatureV2:      cfg.SignatureVersion == SignatureVersionV2,
		BucketLookupType: bucketLookupType,
		AWSSDKAuth:       cfg.AccessKeyID == "",
	}, nil
}

type BucketWithRetries struct {
	bucket           objstore.Bucket
	operationRetries int
	retryMinBackoff  time.Duration
	retryMaxBackoff  time.Duration
}

func (b *BucketWithRetries) retry(ctx context.Context, f func() error) error {
	var lastErr error
	retries := backoff.New(ctx, backoff.Config{
		MinBackoff: b.retryMinBackoff,
		MaxBackoff: b.retryMaxBackoff,
		MaxRetries: b.operationRetries,
	})
	for retries.Ongoing() {
		lastErr = f()
		if lastErr == nil {
			return nil
		}
		if b.bucket.IsObjNotFoundErr(lastErr) {
			return lastErr
		}
		retries.Wait()
	}
	return lastErr
}

func (b *BucketWithRetries) Name() string {
	return b.bucket.Name()
}

func (b *BucketWithRetries) Iter(ctx context.Context, dir string, f func(string) error, options ...objstore.IterOption) error {
	return b.retry(ctx, func() error {
		return b.bucket.Iter(ctx, dir, f, options...)
	})
}

func (b *BucketWithRetries) Get(ctx context.Context, name string) (reader io.ReadCloser, err error) {
	err = b.retry(ctx, func() error {
		reader, err = b.bucket.Get(ctx, name)
		return err
	})
	return
}

func (b *BucketWithRetries) GetRange(ctx context.Context, name string, off, length int64) (closer io.ReadCloser, err error) {
	err = b.retry(ctx, func() error {
		closer, err = b.bucket.GetRange(ctx, name, off, length)
		return err
	})
	return
}

func (b *BucketWithRetries) Exists(ctx context.Context, name string) (exists bool, err error) {
	err = b.retry(ctx, func() error {
		exists, err = b.bucket.Exists(ctx, name)
		return err
	})
	return
}

func (b *BucketWithRetries) Upload(ctx context.Context, name string, r io.Reader) error {
	return b.retry(ctx, func() error {
		return b.bucket.Upload(ctx, name, r)
	})
}

func (b *BucketWithRetries) Attributes(ctx context.Context, name string) (attributes objstore.ObjectAttributes, err error) {
	err = b.retry(ctx, func() error {
		attributes, err = b.bucket.Attributes(ctx, name)
		return err
	})
	return
}

func (b *BucketWithRetries) Delete(ctx context.Context, name string) error {
	return b.retry(ctx, func() error {
		return b.bucket.Delete(ctx, name)
	})
}

func (b *BucketWithRetries) IsObjNotFoundErr(err error) bool {
	return b.bucket.IsObjNotFoundErr(err)
}

func (b *BucketWithRetries) Close() error {
	return b.bucket.Close()
}
