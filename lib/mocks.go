package lib

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/mock"
)

// MockAuditStore mocks the AuditStore interface for testing
type MockAuditStore struct {
	mock.Mock
}

func (m *MockAuditStore) Store(log AuditLog) error {
	args := m.Called(log)
	return args.Error(0)
}

// MockS3Client mocks the S3 client for testing
type MockS3Client struct {
	mock.Mock
}

// Verify MockS3Client implements S3Client interface
var _ S3Client = (*MockS3Client)(nil)

func (m *MockS3Client) PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	args := m.Called(ctx, params)
	return &s3.PutObjectOutput{}, args.Error(1)
}
