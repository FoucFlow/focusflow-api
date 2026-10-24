// Package storage uploads files to the S3-compatible bucket: RustFS on the VPS
// in production (S3_ENDPOINT=http://127.0.0.1:9000), AWS S3 when the endpoint
// is empty. Objects are public-read through Caddy at S3_PUBLIC_ENDPOINT.
package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"
)

var client *s3.Client

// Init builds the client from AWS_REGION, AWS_ACCESS_KEY_ID,
// AWS_SECRET_ACCESS_KEY and S3_ENDPOINT. Without credentials uploads are
// disabled and return an error (the rest of the API keeps working).
func Init() {
	if os.Getenv("AWS_ACCESS_KEY_ID") == "" {
		slog.Warn("storage disabled: AWS_ACCESS_KEY_ID not set (thumbnail and avatar uploads will fail)")
		return
	}
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), "")),
	)
	if err != nil {
		slog.Error("storage disabled: AWS config", "error", err)
		return
	}
	otelaws.AppendMiddlewares(&cfg.APIOptions)

	var opts []func(*s3.Options)
	if endpoint := os.Getenv("S3_ENDPOINT"); endpoint != "" {
		opts = append(opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true // RustFS/MinIO need path-style addressing
		})
	}
	client = s3.NewFromConfig(cfg, opts...)
	slog.Info("storage ready", "endpoint", os.Getenv("S3_ENDPOINT"), "bucket", bucket())
}

// Upload stores data under prefix/name and returns its public URL.
func Upload(ctx context.Context, prefix, name string, data []byte, contentType string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("file storage is not configured")
	}
	key := path.Join(prefix, path.Base(name))
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket()),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", err
	}
	return PublicURL(key), nil
}

// UploadKey stores data under an exact key and returns its public URL.
func UploadKey(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("file storage is not configured")
	}
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket()), Key: aws.String(key),
		Body: bytes.NewReader(data), ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", err
	}
	return PublicURL(key), nil
}

// Get reads an object by key, up to maxBytes.
func Get(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	if client == nil {
		return nil, fmt.Errorf("file storage is not configured")
	}
	out, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket()), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	data, err := io.ReadAll(io.LimitReader(out.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("object is larger than %d bytes", maxBytes)
	}
	return data, nil
}

// DeleteKey removes an object by key.
func DeleteKey(ctx context.Context, key string) {
	if client == nil || key == "" {
		return
	}
	if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket()), Key: aws.String(key)}); err != nil {
		slog.Warn("storage delete failed", "key", key, "error", err)
	}
}

// Delete removes an object given its public URL; URLs outside the bucket are ignored.
func Delete(ctx context.Context, publicURL string) {
	if client == nil {
		return
	}
	prefix := PublicURL("")
	if !strings.HasPrefix(publicURL, prefix) {
		return
	}
	key := strings.TrimPrefix(publicURL, prefix)
	if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket()), Key: aws.String(key)}); err != nil {
		slog.Warn("storage delete failed", "key", key, "error", err)
	}
}

// PublicURL is <S3_PUBLIC_ENDPOINT>/<bucket>/<key>, or the AWS URL without one.
func PublicURL(key string) string {
	if pub := strings.TrimRight(os.Getenv("S3_PUBLIC_ENDPOINT"), "/"); pub != "" {
		return fmt.Sprintf("%s/%s/%s", pub, bucket(), key)
	}
	if ep := strings.TrimRight(os.Getenv("S3_ENDPOINT"), "/"); ep != "" {
		return fmt.Sprintf("%s/%s/%s", ep, bucket(), key)
	}
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucket(), os.Getenv("AWS_REGION"), key)
}

func bucket() string {
	if b := os.Getenv("AWS_BUCKET_NAME"); b != "" {
		return b
	}
	return "focusflow-media"
}
