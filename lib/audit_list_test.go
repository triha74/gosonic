package lib

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileStoreListLogs(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir, false)
	first := AuditLog{
		Project: "app", GitRevision: "aaa", Stage: "build",
		StartTime: time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC), Status: "success",
	}
	second := AuditLog{
		Project: "app", GitRevision: "bbb", Stage: "test",
		StartTime: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC), Status: "success",
	}
	other := AuditLog{
		Project: "other", GitRevision: "aaa", Stage: "build",
		StartTime: time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC), Status: "success",
	}
	require.NoError(t, store.Store(first))
	require.NoError(t, store.Store(second))
	require.NoError(t, store.Store(other))

	logs, err := store.ListLogs("app")
	require.NoError(t, err)
	require.Len(t, logs, 2)
	revisions := map[string]bool{}
	for _, log := range logs {
		assert.Equal(t, "app", log.Project)
		revisions[log.GitRevision] = true
	}
	assert.True(t, revisions["aaa"])
	assert.True(t, revisions["bbb"])

	missing, err := NewFileStore(filepath.Join(dir, "missing"), false).ListLogs("app")
	require.NoError(t, err)
	assert.Empty(t, missing)
}

type pagingS3 struct {
	pages  []*s3.ListObjectsV2Input
	bodies map[string][]byte
	out    []*s3.ListObjectsV2Output
	n      int
}

func (c *pagingS3) PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return &s3.PutObjectOutput{}, nil
}

func (c *pagingS3) ListObjectsV2(_ context.Context, params *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	c.pages = append(c.pages, params)
	page := c.out[c.n]
	c.n++
	return page, nil
}

func (c *pagingS3) GetObject(_ context.Context, params *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(c.bodies[*params.Key]))}, nil
}

func TestS3StoreListLogsPaginates(t *testing.T) {
	first := AuditLog{Project: "app", GitRevision: "aaa", Stage: "build", Status: "success", StartTime: time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)}
	second := AuditLog{Project: "app", GitRevision: "bbb", Stage: "test", Status: "success", StartTime: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)}
	require.NoError(t, signLog(&first))
	require.NoError(t, signLog(&second))
	body := func(log AuditLog) []byte {
		data, err := json.Marshal(log)
		require.NoError(t, err)
		return data
	}
	token := "page-2"
	truncated := true
	client := &pagingS3{
		bodies: map[string][]byte{
			"logs/app-build.json": body(first),
			"logs/app-test.json":  body(second),
		},
		out: []*s3.ListObjectsV2Output{
			{
				IsTruncated:           &truncated,
				NextContinuationToken: &token,
				Contents:              []types.Object{{Key: strPtr("logs/app-build.json")}},
			},
			{
				Contents: []types.Object{{Key: strPtr("logs/app-test.json")}},
			},
		},
	}
	store := NewS3Store(client, "bucket", "logs", false)
	logs, err := store.ListLogs("app")
	require.NoError(t, err)
	require.Len(t, logs, 2)
	assert.Equal(t, "aaa", logs[0].GitRevision)
	assert.Equal(t, "bbb", logs[1].GitRevision)
	require.Len(t, client.pages, 2)
	assert.Nil(t, client.pages[0].ContinuationToken)
	require.NotNil(t, client.pages[1].ContinuationToken)
	assert.Equal(t, "page-2", *client.pages[1].ContinuationToken)
}

func strPtr(value string) *string {
	return &value
}
