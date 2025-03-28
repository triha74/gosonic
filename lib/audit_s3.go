package lib

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Client defines the interface for S3 operations we need
type S3Client interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// S3Store implements AuditStore using AWS S3
type S3Store struct {
	Client     S3Client
	BucketName string
	Prefix     string // Optional prefix for S3 keys
	Verbose    bool   // Enable verbose logging
}

// NewS3Store creates a new S3Store with the given client and bucket
func NewS3Store(client S3Client, bucketName string, prefix string, verbose bool) *S3Store {
	return &S3Store{
		Client:     client,
		BucketName: bucketName,
		Prefix:     prefix,
		Verbose:    verbose,
	}
}

// Store implements AuditStore for S3Store
func (s *S3Store) Store(log AuditLog) error {
	data, err := log.marshalLog()
	if err != nil {
		return fmt.Errorf("marshaling audit log: %w", err)
	}

	key := log.generateFilename()
	if s.Prefix != "" {
		key = filepath.Join(s.Prefix, key)
	}

	// Create the PutObject input
	input := &s3.PutObjectInput{
		Bucket: &s.BucketName,
		Key:    &key,
		Body:   bytes.NewReader(data),
	}

	// Attempt to put the object
	_, err = s.Client.PutObject(context.Background(), input)
	if err != nil {
		if s.Verbose {
			fmt.Printf("Error uploading to S3: %v\n", err)

			// Check if it's a credentials error
			if strings.Contains(err.Error(), "operation error S3: PutObject, https response error StatusCode: 403") {
				fmt.Println("\nAWS Credential Troubleshooting:")
				fmt.Println("1. Check if AWS_PROFILE or AUDIT_LOG_AWS_PROFILE is set correctly")
				fmt.Println("2. Verify credentials in ~/.aws/credentials")
				fmt.Println("3. Check if AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are set")
				fmt.Println("4. Ensure the IAM user/role has s3:PutObject permission for the bucket")
			}
		}
		return fmt.Errorf("uploading audit log to S3: %w", err)
	}

	if s.Verbose {
		fmt.Printf("Successfully stored audit log to S3: %s/%s\n", s.BucketName, key)
	}

	return nil
}

// LoadLogs implements AuditStore for S3Store
func (s *S3Store) LoadLogs(project, gitRevision string) ([]AuditLog, error) {
	var logs []AuditLog

	if s.Verbose {
		fmt.Printf("Loading audit logs from S3 bucket '%s' for project '%s' and revision '%s'\n",
			s.BucketName, project, gitRevision)
	}

	// Create prefix for listing objects
	prefix := project + "-"
	if s.Prefix != "" {
		prefix = filepath.Join(s.Prefix, prefix)
	}

	// List objects in the bucket with the project prefix
	input := &s3.ListObjectsV2Input{
		Bucket: &s.BucketName,
		Prefix: &prefix,
	}

	result, err := s.Client.ListObjectsV2(context.Background(), input)
	if err != nil {
		return nil, fmt.Errorf("listing objects in S3 bucket: %w", err)
	}

	// Process each matching object
	for _, obj := range result.Contents {
		// Skip if not a JSON file
		if !strings.HasSuffix(*obj.Key, ".json") {
			continue
		}

		// Get the object
		getInput := &s3.GetObjectInput{
			Bucket: &s.BucketName,
			Key:    obj.Key,
		}

		output, err := s.Client.GetObject(context.Background(), getInput)
		if err != nil {
			return nil, fmt.Errorf("getting object from S3: %w", err)
		}

		// Read the object body
		data, err := io.ReadAll(output.Body)
		output.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading object body: %w", err)
		}

		// Parse the log
		var log AuditLog
		if err := json.Unmarshal(data, &log); err != nil {
			return nil, fmt.Errorf("parsing log file %s: %w", *obj.Key, err)
		}

		// Only include logs for the specified git revision
		if log.GitRevision == gitRevision {
			logs = append(logs, log)
		}
	}

	if s.Verbose {
		fmt.Printf("Loaded %d audit logs\n", len(logs))
	}

	return logs, nil
}
