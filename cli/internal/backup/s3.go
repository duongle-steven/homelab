package backup

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// listBucket sums the sizes of every object in bucket (R2 and the sandbox's versitygw both speak S3, path style).
func listBucket(ctx context.Context, creds Credentials, bucket string) (int64, error) {
	client := s3.New(s3.Options{
		Region:       "auto",
		BaseEndpoint: aws.String(creds.Endpoint),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(creds.AccessKey, creds.SecretKey, ""),
	})
	var total int64
	pages := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return 0, fmt.Errorf("list bucket %s: %w", bucket, err)
		}
		for _, o := range page.Contents {
			total += aws.ToInt64(o.Size)
		}
	}
	return total, nil
}
