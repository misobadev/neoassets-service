package r2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/rs/zerolog"
)

// Client performs object operations against a Cloudflare R2 bucket using the
// S3-compatible API. The service signs URLs and the client uploads/downloads
// directly.
type Client interface {
	GenerateSignedUploadURL(ctx context.Context, objectName, contentType string, expiration time.Duration) (string, error)
	DeleteObject(ctx context.Context, objectName string) error
	UploadFile(ctx context.Context, objectName, contentType string, reader io.Reader) error
	UploadFileCached(ctx context.Context, objectName, contentType, cacheControl string, reader io.Reader) error
	CopyObject(ctx context.Context, sourceKey, destKey string) error
	CopyObjectCached(ctx context.Context, sourceKey, destKey, cacheControl string) error
	DownloadObject(ctx context.Context, objectName string) ([]byte, error)
	ListKeys(ctx context.Context, prefix string) ([]string, error)
	ListKeysWithSize(ctx context.Context, prefix string) ([]ObjectInfo, error)
	DeleteObjects(ctx context.Context, keys []string) error
	ObjectExists(ctx context.Context, objectName string) (bool, error)
	ObjectSize(ctx context.Context, objectName string) (int64, bool, error)
}

type ObjectInfo struct {
	Key  string
	Size int64
}

type clientImpl struct {
	bucketName    string
	presignClient *s3.PresignClient
	s3Client      *s3.Client
	log           zerolog.Logger
}

// NewClient creates a Cloudflare R2 client from an account id, API keys, and
// the bucket name to operate on.
func NewClient(ctx context.Context, accountID, accessKey, secretKey, bucketName string, log zerolog.Logger) (Client, error) {
	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)

	r2Resolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		return aws.Endpoint{
			URL:               endpoint,
			HostnameImmutable: true,
			SigningRegion:     "auto",
		}, nil
	})

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithEndpointResolverWithOptions(r2Resolver),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
		config.WithRegion("auto"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load R2 config: %w", err)
	}

	client := s3.NewFromConfig(cfg)

	log.Info().Str("account", accountID).Str("bucket", bucketName).Msg("R2 client initialized")

	return &clientImpl{
		bucketName:    bucketName,
		presignClient: s3.NewPresignClient(client),
		s3Client:      client,
		log:           log,
	}, nil
}

// GenerateSignedUploadURL returns a time-limited presigned PUT URL so the
// client can upload directly to R2 without exposing credentials.
func (c *clientImpl) GenerateSignedUploadURL(ctx context.Context, objectName, contentType string, expiration time.Duration) (string, error) {
	req, err := c.presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(c.bucketName),
		Key:         aws.String(objectName),
		ContentType: aws.String(contentType),
	}, s3.WithPresignExpires(expiration))
	if err != nil {
		return "", fmt.Errorf("failed to generate signed upload URL: %w", err)
	}

	c.log.Debug().
		Str("bucket", c.bucketName).
		Str("object", objectName).
		Dur("expires_in", expiration).
		Msg("signed upload URL generated (R2)")

	return req.URL, nil
}

// DeleteObject removes a single object from the bucket.
func (c *clientImpl) DeleteObject(ctx context.Context, objectName string) error {
	_, err := c.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucketName),
		Key:    aws.String(objectName),
	})
	if err != nil {
		return fmt.Errorf("failed to delete object %s/%s: %w", c.bucketName, objectName, err)
	}

	c.log.Info().Str("bucket", c.bucketName).Str("object", objectName).Msg("R2 object deleted")
	return nil
}

// UploadFile uploads a file directly to the bucket (used for internal writes
// such as the generated manifest).
func (c *clientImpl) UploadFile(ctx context.Context, objectName, contentType string, reader io.Reader) error {
	return c.UploadFileCached(ctx, objectName, contentType, "", reader)
}

// UploadFileCached uploads a file with an explicit Cache-Control header.
func (c *clientImpl) UploadFileCached(ctx context.Context, objectName, contentType, cacheControl string, reader io.Reader) error {
	in := &s3.PutObjectInput{
		Bucket:      aws.String(c.bucketName),
		Key:         aws.String(objectName),
		Body:        reader,
		ContentType: aws.String(contentType),
	}
	if cacheControl != "" {
		in.CacheControl = aws.String(cacheControl)
	}
	if _, err := c.s3Client.PutObject(ctx, in); err != nil {
		return fmt.Errorf("failed to upload object %s: %w", objectName, err)
	}
	return nil
}

// CopyObject copies an object server-side (used to move approved submission
// media to its canonical location).
func (c *clientImpl) CopyObject(ctx context.Context, sourceKey, destKey string) error {
	return c.CopyObjectCached(ctx, sourceKey, destKey, "")
}

// CopyObjectCached copies an object server-side and replaces its metadata,
// setting an explicit Cache-Control header on the destination.
func (c *clientImpl) CopyObjectCached(ctx context.Context, sourceKey, destKey, cacheControl string) error {
	in := &s3.CopyObjectInput{
		Bucket:     aws.String(c.bucketName),
		CopySource: aws.String(c.bucketName + "/" + sourceKey),
		Key:        aws.String(destKey),
	}
	if cacheControl != "" {
		in.CacheControl = aws.String(cacheControl)
		in.MetadataDirective = types.MetadataDirectiveReplace
	}
	if _, err := c.s3Client.CopyObject(ctx, in); err != nil {
		return fmt.Errorf("failed to copy object %s -> %s: %w", sourceKey, destKey, err)
	}
	c.log.Info().Str("bucket", c.bucketName).Str("src", sourceKey).Str("dst", destKey).Msg("R2 object copied")
	return nil
}

// DownloadObject reads the full contents of an object (used to fetch a user's
// uploaded video before converting it with ffmpeg).
func (c *clientImpl) DownloadObject(ctx context.Context, objectName string) ([]byte, error) {
	out, err := c.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucketName),
		Key:    aws.String(objectName),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to download object %s: %w", objectName, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read object %s: %w", objectName, err)
	}
	return data, nil
}

// ListKeys returns all object keys under a prefix (used to clean up orphaned
// pack objects when a submission is trashed or rejected).
func (c *clientImpl) ListKeys(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	paginator := s3.NewListObjectsV2Paginator(c.s3Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.bucketName),
		Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list objects %s: %w", prefix, err)
		}
		for _, o := range page.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
	}
	return keys, nil
}

// ListKeysWithSize returns all object keys and their sizes under a prefix.
func (c *clientImpl) ListKeysWithSize(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var infos []ObjectInfo
	paginator := s3.NewListObjectsV2Paginator(c.s3Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.bucketName),
		Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list objects %s: %w", prefix, err)
		}
		for _, o := range page.Contents {
			var sz int64
			if o.Size != nil {
				sz = *o.Size
			}
			infos = append(infos, ObjectInfo{
				Key:  aws.ToString(o.Key),
				Size: sz,
			})
		}
	}
	return infos, nil
}

// DeleteObjects removes multiple objects from the bucket (S3 batch delete).
func (c *clientImpl) DeleteObjects(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	var failed []string
	for i := 0; i < len(keys); i += 1000 {
		end := i + 1000
		if end > len(keys) {
			end = len(keys)
		}
		objs := make([]types.ObjectIdentifier, 0, end-i)
		for _, k := range keys[i:end] {
			objs = append(objs, types.ObjectIdentifier{Key: aws.String(k)})
		}
		out, err := c.s3Client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(c.bucketName),
			Delete: &types.Delete{Objects: objs},
		})
		if err != nil {
			return fmt.Errorf("failed to delete objects: %w", err)
		}
		for _, e := range out.Errors {
			failed = append(failed, aws.ToString(e.Key))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("failed to delete objects: %v", failed)
	}
	c.log.Info().Str("bucket", c.bucketName).Int("keys", len(keys)).Msg("R2 objects deleted")
	return nil
}

// ObjectExists reports whether an object is present in the bucket (used to
// verify a browser upload actually persisted before it is registered).
func (c *clientImpl) ObjectExists(ctx context.Context, objectName string) (bool, error) {
	_, err := c.s3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucketName),
		Key:    aws.String(objectName),
	})
	if err == nil {
		return true, nil
	}
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return false, nil
	}
	// S3-compatible stores may return a generic 404 that does not unwrap to
	// types.NotFound; treat any head error as "likely missing" so the backend
	// refuses to register an object that is not actually there.
	return false, nil
}

// ObjectSize returns an object's size in bytes and whether it exists. Used to
// enforce per-format size limits (e.g. GIFs) on the stored object, since the
// client-declared size can be spoofed.
func (c *clientImpl) ObjectSize(ctx context.Context, objectName string) (int64, bool, error) {
	out, err := c.s3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucketName),
		Key:    aws.String(objectName),
	})
	if err != nil {
		var nf *types.NotFound
		if errors.As(err, &nf) {
			return 0, false, nil
		}
		return 0, false, nil
	}
	if out.ContentLength != nil {
		return *out.ContentLength, true, nil
	}
	return 0, true, nil
}
