package lib

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Allow mocking exec.Command in tests
var execCommand = exec.Command

// S3Client defines the interface for S3 operations we need
type S3Client interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// AuditStore defines the interface for audit log persistence
type AuditStore interface {
	// Store persists the audit log
	Store(log AuditLog) error
}

// FileStore implements AuditStore using the local filesystem
type FileStore struct {
	Directory string // Directory where logs will be stored
}

// S3Store implements AuditStore using AWS S3
type S3Store struct {
	Client     S3Client
	BucketName string
	Prefix     string // Optional prefix for S3 keys
}

type AuditLog struct {
	Project     string    `json:"project"`
	GitRevision string    `json:"git_revision"`
	Stage       string    `json:"stage"`
	Command     string    `json:"command"`
	StartTime   time.Time `json:"start_time"`
	Duration    float64   `json:"duration"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
}

// generateFilename creates a consistent filename for the audit log
func (a AuditLog) generateFilename() string {
	return fmt.Sprintf("%s-%s-%s.json",
		a.Project,
		a.Stage,
		a.StartTime.Format("20060102-150405"),
	)
}

// marshalLog converts the audit log to JSON bytes
func (a AuditLog) marshalLog() ([]byte, error) {
	return json.MarshalIndent(a, "", "  ")
}

func (a *AuditLog) SetError(err error) {
	a.Status = "error"
	a.Error = err.Error()
}

func GetGitRevision() (string, error) {
	cmd := execCommand("git", "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Store implements AuditStore for FileStore
func (fs *FileStore) Store(log AuditLog) error {
	data, err := log.marshalLog()
	if err != nil {
		return fmt.Errorf("marshaling audit log: %w", err)
	}

	// Create logs directory if it doesn't exist
	if err := os.MkdirAll(fs.Directory, 0755); err != nil {
		return fmt.Errorf("creating logs directory: %w", err)
	}

	logPath := filepath.Join(fs.Directory, log.generateFilename())

	if err := os.WriteFile(logPath, data, 0644); err != nil {
		return fmt.Errorf("writing audit log: %w", err)
	}

	return nil
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

	_, err = s.Client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: &s.BucketName,
		Key:    &key,
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("uploading audit log to S3: %w", err)
	}

	return nil
}

// NewFileStore creates a new FileStore with the given directory
func NewFileStore(directory string) *FileStore {
	return &FileStore{
		Directory: directory,
	}
}

// NewS3Store creates a new S3Store with the given client and bucket
func NewS3Store(client S3Client, bucketName string, prefix string) *S3Store {
	return &S3Store{
		Client:     client,
		BucketName: bucketName,
		Prefix:     prefix,
	}
}
