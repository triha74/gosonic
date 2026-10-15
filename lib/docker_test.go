package lib

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// Mock docker execution for tests
var mockDockerExec func(args []string) DockerResult

func init() {
	// Store the original docker execution function
	originalExec := ExecDocker
	// Set up the mock wrapper
	ExecDocker = func(args []string) DockerResult {
		if mockDockerExec != nil {
			return mockDockerExec(args)
		}
		return originalExec(args)
	}
}

func TestExecDocker(t *testing.T) {
	tests := map[string]struct {
		args       []string
		wantStdout string
		wantErr    bool
	}{
		"successful command": {
			args:       []string{"docker", "version", "--format", "{{.Server.Version}}"},
			wantStdout: "", // actual version will vary
			wantErr:    false,
		},
		"failed command": {
			args:    []string{"docker", "invalid-command"},
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			result := ExecDocker(tc.args)

			if tc.wantErr {
				assert.Error(t, result.Error)
				assert.NotZero(t, result.ExitCode)
			} else {
				assert.NoError(t, result.Error)
				assert.Zero(t, result.ExitCode)
				if tc.wantStdout != "" {
					assert.Equal(t, tc.wantStdout, strings.TrimSpace(result.Stdout))
				}
			}
		})
	}
}

func TestParseImageRef(t *testing.T) {
	tests := map[string]struct {
		input string
		want  ImageRef
	}{
		"full reference with tag": {
			input: "docker.io/library/golang:1.22",
			want: ImageRef{
				Domain:      "docker.io",
				ContextPath: "library",
				Name:        "golang",
				Tag:         "1.22",
			},
		},
		"with digest": {
			input: "public.ecr.aws/docker/library/alpine@sha256:123",
			want: ImageRef{
				Domain:      "public.ecr.aws",
				ContextPath: "docker/library",
				Name:        "alpine",
				Digest:      "sha256:123",
			},
		},
		"simple name": {
			input: "golang",
			want: ImageRef{
				Name: "golang",
			},
		},
		"with context": {
			input: "library/golang",
			want: ImageRef{
				ContextPath: "library",
				Name:        "golang",
			},
		},
		"with port": {
			input: "localhost:5000/golang:1.22",
			want: ImageRef{
				Domain: "localhost:5000",
				Name:   "golang",
				Tag:    "1.22",
			},
		},
		"empty string": {
			input: "",
			want:  ImageRef{},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := ParseImageRef(tc.input)
			assert.Equal(t, tc.want, got)

			// Test round trip
			if tc.input != "" {
				parsed := ParseImageRef(got.String())
				assert.Equal(t, got, parsed, "Round trip parsing should match")
			}
		})
	}
}

func TestResolveRunnerImage(t *testing.T) {
	defaultRegistry := "public.ecr.aws"
	tests := map[string]struct {
		runner string
		want   string
	}{
		"full path": {
			runner: "registry.example.com/golang:1.22",
			want:   "registry.example.com/golang:1.22",
		},
		"simple name with version": {
			runner: "golang:1.22",
			want:   "public.ecr.aws/golang:1.22",
		},
		"with context path": {
			runner: "docker/library/golang:1.22",
			want:   "public.ecr.aws/docker/library/golang:1.22",
		},
		"without version": {
			runner: "golang",
			want:   "public.ecr.aws/golang",
		},
		"with registry port": {
			runner: "localhost:5000/golang:1.22",
			want:   "localhost:5000/golang:1.22",
		},
		"with organization": {
			runner: "library/golang:1.22",
			want:   "public.ecr.aws/library/golang:1.22",
		},
		"empty string": {
			runner: "",
			want:   "public.ecr.aws/docker/library/alpine:latest",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := ResolveRunnerImage(tc.runner, defaultRegistry)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseImageRef_UnitTests(t *testing.T) {
	tests := map[string]struct {
		input string
		want  ImageRef
	}{
		"official image": {
			input: "ubuntu",
			want: ImageRef{
				Name: "ubuntu",
			},
		},
		"with tag": {
			input: "ubuntu:20.04",
			want: ImageRef{
				Name: "ubuntu",
				Tag:  "20.04",
			},
		},
		"with digest": {
			input: "ubuntu@sha256:45b23dee08af5e43a7fea6c4cf9c25ccf269ee113168c19722f87876677c5cb2",
			want: ImageRef{
				Name:   "ubuntu",
				Digest: "sha256:45b23dee08af5e43a7fea6c4cf9c25ccf269ee113168c19722f87876677c5cb2",
			},
		},
		"with tag and digest": {
			input: "ubuntu:20.04@sha256:45b23dee08af5e43a7fea6c4cf9c25ccf269ee113168c19722f87876677c5cb2",
			want: ImageRef{
				Name:   "ubuntu",
				Tag:    "20.04",
				Digest: "sha256:45b23dee08af5e43a7fea6c4cf9c25ccf269ee113168c19722f87876677c5cb2",
			},
		},
		"with registry": {
			input: "registry.example.com/ubuntu",
			want: ImageRef{
				Domain: "registry.example.com",
				Name:   "ubuntu",
			},
		},
		"with registry and port": {
			input: "localhost:5000/ubuntu",
			want: ImageRef{
				Domain: "localhost:5000",
				Name:   "ubuntu",
			},
		},
		"with registry and tag": {
			input: "registry.example.com/ubuntu:20.04",
			want: ImageRef{
				Domain: "registry.example.com",
				Name:   "ubuntu",
				Tag:    "20.04",
			},
		},
		"with registry and digest": {
			input: "registry.example.com/ubuntu@sha256:45b23dee08af5e43a7fea6c4cf9c25ccf269ee113168c19722f87876677c5cb2",
			want: ImageRef{
				Domain: "registry.example.com",
				Name:   "ubuntu",
				Digest: "sha256:45b23dee08af5e43a7fea6c4cf9c25ccf269ee113168c19722f87876677c5cb2",
			},
		},
		"with organization": {
			input: "organization/repository",
			want: ImageRef{
				ContextPath: "organization",
				Name:        "repository",
			},
		},
		"with deep organization": {
			input: "org/suborg/repository",
			want: ImageRef{
				ContextPath: "org/suborg",
				Name:        "repository",
			},
		},
		"complex path": {
			input: "registry.example.com/org/suborg/repository:tag@sha256:digest",
			want: ImageRef{
				Domain:      "registry.example.com",
				ContextPath: "org/suborg",
				Name:        "repository",
				Tag:         "tag",
				Digest:      "sha256:digest",
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := ParseImageRef(tc.input)
			assert.Equal(t, tc.want, got)

			// Test round trip parsing
			if tc.input != "" {
				roundTrip := ParseImageRef(got.String())
				assert.Equal(t, got, roundTrip, "Round trip parsing should match")
			}
		})
	}
}

type mockAuditStore struct {
	logs []AuditLog
}

func (m *mockAuditStore) Store(log AuditLog) error {
	m.logs = append(m.logs, log)
	return nil
}

func (m *mockAuditStore) LoadLogs(project, gitRevision string) ([]AuditLog, error) {
	var result []AuditLog
	for _, log := range m.logs {
		if log.Project == project && log.GitRevision == gitRevision {
			result = append(result, log)
		}
	}
	return result, nil
}

func TestExecuteStage(t *testing.T) {
	// Store original ExecDocker
	originalExecDocker := ExecDocker
	defer func() { ExecDocker = originalExecDocker }()

	tests := []struct {
		name        string
		stage       StageExecution
		wantCommand []string
		wantErr     bool
	}{
		{
			name: "single command - direct execution",
			stage: StageExecution{
				Name:     "test",
				Runner:   "alpine:latest",
				Commands: []string{"echo hello"},
			},
			wantCommand: []string{
				"docker", "run", "--rm", "--init", "--workdir", "/workspace",
				"alpine:latest", "echo", "hello", // Direct execution
			},
			wantErr: false,
		},
		{
			name: "multiple commands - shell execution",
			stage: StageExecution{
				Name:     "test",
				Runner:   "alpine:latest",
				Commands: []string{"echo hello", "echo world"},
			},
			wantCommand: []string{
				"docker", "run", "--rm", "--init", "--workdir", "/workspace",
				"alpine:latest", "sh", "-c", "echo hello && echo world", // Shell execution
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Mock docker execution
			ExecDocker = func(args []string) DockerResult {
				// Verify command structure
				assert.Equal(t, tc.wantCommand, args)
				return DockerResult{ExitCode: 0}
			}

			// Create a mock audit store
			mockStore := new(MockAuditStore)
			mockStore.On("Store", mock.AnythingOfType("AuditLog")).Return(nil)

			// Execute stage
			err := ExecuteStage(tc.stage, mockStore, "test-project")

			// Check error
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestExecuteStageSuccessIsRecordedAfterExit(t *testing.T) {
	originalExecDocker := ExecDocker
	defer func() { ExecDocker = originalExecDocker }()

	started := make(chan struct{})
	release := make(chan struct{})
	ExecDocker = func(args []string) DockerResult {
		close(started)
		<-release
		return DockerResult{Stdout: "ok\n"}
	}

	store := &mockAuditStore{}
	done := make(chan error, 1)
	go func() {
		done <- ExecuteStage(StageExecution{
			Name:     "build",
			Runner:   "alpine:latest",
			Commands: []string{"echo ok"},
		}, store, "proj")
	}()

	<-started
	if assert.Len(t, store.logs, 1) {
		assert.Equal(t, "running", store.logs[0].Status)
	}
	close(release)
	assert.NoError(t, <-done)
	if assert.Len(t, store.logs, 2) {
		assert.Equal(t, "success", store.logs[1].Status)
		assert.Equal(t, store.logs[0].StartTime, store.logs[1].StartTime)
		assert.Positive(t, store.logs[1].Duration)
	}
}

func TestExecuteStagePrefixesOutput(t *testing.T) {
	originalExecDocker := ExecDocker
	defer func() { ExecDocker = originalExecDocker }()
	ExecDocker = func(args []string) DockerResult {
		return DockerResult{Stdout: "hello\n", Stderr: "warn\n"}
	}

	var mu sync.Mutex
	stdout, stderr := captureFD(t, func() {
		err := ExecuteStage(StageExecution{
			Name:         "build",
			Runner:       "alpine:latest",
			Commands:     []string{"echo hello"},
			PrefixOutput: true,
			OutputLock:   &mu,
		}, nil, "proj")
		assert.NoError(t, err)
	})
	assert.Contains(t, stdout, "build | hello\n")
	assert.Contains(t, stderr, "build | warn\n")
}

func captureFD(t *testing.T, fn func()) (string, string) {
	t.Helper()
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	rOut, wOut, err := os.Pipe()
	assert.NoError(t, err)
	rErr, wErr, err := os.Pipe()
	assert.NoError(t, err)
	os.Stdout = wOut
	os.Stderr = wErr

	outC := make(chan string)
	errC := make(chan string)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, rOut)
		outC <- buf.String()
	}()
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, rErr)
		errC <- buf.String()
	}()

	fn()
	wOut.Close()
	wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	return <-outC, <-errC
}
