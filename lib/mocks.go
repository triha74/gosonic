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

// Store mocks the Store method
func (m *MockAuditStore) Store(log AuditLog) error {
	args := m.Called(log)
	return args.Error(0)
}

// LoadLogs mocks the LoadLogs method
func (m *MockAuditStore) LoadLogs(project, gitRevision string) ([]AuditLog, error) {
	args := m.Called(project, gitRevision)
	return args.Get(0).([]AuditLog), args.Error(1)
}

// MockS3Client mocks the S3 client for testing
type MockS3Client struct {
	mock.Mock
}

// Verify MockS3Client implements S3Client interface
var _ S3Client = (*MockS3Client)(nil)

// PutObject mocks the PutObject method
func (m *MockS3Client) PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	args := m.Called(ctx, params)
	return &s3.PutObjectOutput{}, args.Error(1)
}

// ListObjectsV2 mocks the ListObjectsV2 method
func (m *MockS3Client) ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	args := m.Called(ctx, params)
	return &s3.ListObjectsV2Output{}, args.Error(1)
}

// GetObject mocks the GetObject method
func (m *MockS3Client) GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	args := m.Called(ctx, params)
	return &s3.GetObjectOutput{}, args.Error(1)
}
