package lib

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// Mock command execution
var mockExecCommand = exec.Command

func init() {
	// Override exec.Command with our mock
	execCommand = mockExecCommand
}

func TestFileStore(t *testing.T) {
	// Create a temporary directory for logs
	tmpDir := t.TempDir()
	store := NewFileStore(filepath.Join(tmpDir, "logs"))

	log := AuditLog{
		Project:     "test-project",
		GitRevision: "abc123",
		Stage:       "test",
		Command:     "echo hello",
		StartTime:   time.Now(),
		Status:      "success",
		Duration:    1.5,
	}

	// Test SetError
	t.Run("set error", func(t *testing.T) {
		log.SetError(assert.AnError)
		assert.Equal(t, "error", log.Status)
		assert.Equal(t, assert.AnError.Error(), log.Error)
	})

	// Test WriteAuditLog
	t.Run("write log", func(t *testing.T) {
		err := store.Store(log)
		assert.NoError(t, err)

		// Check if file exists
		files, err := os.ReadDir(store.Directory)
		assert.NoError(t, err)
		assert.Len(t, files, 1)

		// Read and verify content
		content, err := os.ReadFile(filepath.Join(store.Directory, files[0].Name()))
		assert.NoError(t, err)

		var readLog AuditLog
		err = json.Unmarshal(content, &readLog)
		assert.NoError(t, err)
		assert.Equal(t, log.Project, readLog.Project)
		assert.Equal(t, log.Stage, readLog.Stage)
		assert.Equal(t, log.Status, readLog.Status)
	})
}

func TestS3Store(t *testing.T) {
	mockClient := new(MockS3Client)
	store := NewS3Store(mockClient, "test-bucket", "logs")

	log := AuditLog{
		Project:     "test-project",
		GitRevision: "abc123",
		Stage:       "test",
		Command:     "echo hello",
		StartTime:   time.Now(),
		Status:      "success",
		Duration:    1.5,
	}

	expectedKey := filepath.Join("logs", log.generateFilename())
	expectedData, _ := log.marshalLog()

	// Set up expectations
	mockClient.On("PutObject", mock.Anything, &s3.PutObjectInput{
		Bucket: aws.String("test-bucket"),
		Key:    aws.String(expectedKey),
		Body:   bytes.NewReader(expectedData),
	}).Return(&s3.PutObjectOutput{}, nil)

	err := store.Store(log)
	assert.NoError(t, err)

	mockClient.AssertExpectations(t)
}

func TestGetGitRevision(t *testing.T) {
	// Mock git command
	mockSHA := "0123456789abcdef0123456789abcdef01234567"
	execCommand = func(command string, args ...string) *exec.Cmd {
		if command == "git" && len(args) > 0 && args[0] == "rev-parse" {
			// Create a mock command that outputs our mock SHA
			return exec.Command("echo", mockSHA)
		}
		return mockExecCommand(command, args...)
	}
	defer func() { execCommand = mockExecCommand }()

	rev, err := GetGitRevision()
	assert.NoError(t, err)
	assert.Equal(t, mockSHA, rev)
}
