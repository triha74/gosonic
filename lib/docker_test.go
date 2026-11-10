package lib

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

	user := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	for i := range tests {
		tests[i].wantCommand = append([]string{
			"docker", "run", "--rm", "--init", "--workdir", "/workspace",
			"--user", user,
			"--cap-drop", "ALL",
			"--security-opt", "no-new-privileges",
			"--pids-limit", "4096",
		}, tests[i].wantCommand[6:]...)
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

func TestDockerRunOptions(t *testing.T) {
	original := ExecDocker
	defer func() { ExecDocker = original }()

	var calls [][]string
	ExecDocker = func(args []string) DockerResult {
		copied := append([]string{}, args...)
		calls = append(calls, copied)
		return DockerResult{}
	}

	err := ExecuteStage(StageExecution{
		Name:              "build",
		Runner:            "alpine:latest",
		Commands:          []string{"echo hi"},
		WorkingDirectory:  "/workspace/src",
		DockerSocket:      true,
		AllowDockerSocket: true,
		Volumes: []Volume{{
			Type:   "cache",
			Source: "cargo registry",
			Target: "/root/.cargo/registry",
		}},
		Services: []ServiceSpec{{
			Name:  "db",
			Image: "postgres:16",
			Ports: []string{"5432:5432"},
		}},
	}, nil, "proj")
	assert.NoError(t, err)
	requireCalls := func() []string {
		for _, call := range calls {
			if len(call) > 2 && call[1] == "run" && call[2] != "-d" {
				return call
			}
		}
		t.Fatal("stage container was not started")
		return nil
	}
	stageRun := requireCalls()
	assert.Contains(t, calls[0], "network")
	assert.Contains(t, calls[1], "postgres:16")
	assert.Contains(t, stageRun, "--workdir")
	assert.Contains(t, stageRun, "/workspace/src")
	assert.Contains(t, stageRun, "--network")
	assert.Contains(t, stageRun, "gosonic-cache-cargo-registry:/root/.cargo/registry")
	assert.Contains(t, stageRun, "/var/run/docker.sock:/var/run/docker.sock")
	assert.Equal(t, "network", calls[len(calls)-1][1])
}

func TestExecuteStageTimeout(t *testing.T) {
	original := ExecDockerCtx
	defer func() { ExecDockerCtx = original }()
	ExecDockerCtx = func(ctx context.Context, args []string) DockerResult {
		<-ctx.Done()
		return DockerResult{Error: ctx.Err(), ExitCode: 1}
	}

	err := ExecuteStage(StageExecution{
		Name:     "slow",
		Runner:   "alpine:latest",
		Commands: []string{"sleep 30"},
		Timeout:  20 * time.Millisecond,
	}, nil, "proj")
	assert.Error(t, err)
}

func TestCopiedWorkspaceWhenMountDenied(t *testing.T) {
	original := ExecDocker
	originalCopy := copyWorkspaceToVolume
	defer func() {
		ExecDocker = original
		copyWorkspaceToVolume = originalCopy
	}()
	var calls [][]string
	ExecDocker = func(args []string) DockerResult {
		calls = append(calls, append([]string{}, args...))
		if len(calls) == 1 {
			return DockerResult{Error: assert.AnError, Stderr: "mounts denied: The path /workspaces/app is not shared from the host\n", ExitCode: 125}
		}
		return DockerResult{}
	}
	var copied []string
	copyWorkspaceToVolume = func(image, volume, target string) DockerResult {
		copied = []string{image, volume, target}
		return DockerResult{}
	}
	err := ExecuteStage(StageExecution{
		Name:     "build",
		Runner:   "alpine:latest",
		Commands: []string{"echo hi"},
		Volumes:  []Volume{{Type: "bind", Source: ".", Target: "/workspace"}},
	}, nil, "proj")
	assert.NoError(t, err)
	assert.Equal(t, []string{"alpine:latest", calls[1][len(calls[1])-1], "/workspace"}, copied)
	assert.Equal(t, "volume", calls[1][1])
	assert.Equal(t, "create", calls[1][2])
	assert.Equal(t, "run", calls[2][1])
	assert.NotContains(t, calls[2], ".:/workspace")
	assert.Contains(t, calls[2], copied[1]+":/workspace")
}

func TestShellCommandsKeepContainerVariables(t *testing.T) {
	original := ExecDocker
	defer func() { ExecDocker = original }()
	var got []string
	ExecDocker = func(args []string) DockerResult {
		got = append([]string{}, args...)
		return DockerResult{}
	}
	err := ExecuteStage(StageExecution{
		Name:     "test",
		Runner:   "alpine:latest",
		Commands: []string{"echo $shard", "cargo test"},
	}, nil, "proj")
	assert.NoError(t, err)
	assert.Contains(t, got, "sh")
	assert.Contains(t, got, "echo $shard && cargo test")
}

func TestMultilineCommandsJoinOnTheSameStatement(t *testing.T) {
	original := ExecDocker
	defer func() { ExecDocker = original }()
	var got []string
	ExecDocker = func(args []string) DockerResult {
		got = append([]string{}, args...)
		return DockerResult{}
	}
	err := ExecuteStage(StageExecution{
		Name:   "clients",
		Runner: "ubuntu:24.04",
		Shell:  "bash",
		Commands: []string{
			"cargo build -p cypherlite-ffi",
			"for src in *.c; do\n  cc \"$src\"\ndone\n",
			"cd clients/javascript && npm test",
		},
	}, nil, "proj")
	assert.NoError(t, err)
	script := "cargo build -p cypherlite-ffi && for src in *.c; do\n  cc \"$src\"\ndone && cd clients/javascript && npm test"
	assert.Contains(t, got, script)
	assert.NoError(t, exec.Command("bash", "-n", "-c", script).Run())
}

func TestShellCommandsCanUseBash(t *testing.T) {
	original := ExecDocker
	defer func() { ExecDocker = original }()
	var got []string
	ExecDocker = func(args []string) DockerResult {
		got = append([]string{}, args...)
		return DockerResult{}
	}
	err := ExecuteStage(StageExecution{
		Name:     "test",
		Runner:   "ubuntu:24.04",
		Shell:    "bash",
		Commands: []string{"set -euo pipefail", "echo $shard"},
	}, nil, "proj")
	assert.NoError(t, err)
	assert.Contains(t, got, "bash")
	assert.NotContains(t, got, "sh")
	assert.Contains(t, got, "set -euo pipefail && echo $shard")
}

func TestFormatCommandQuotesShellScript(t *testing.T) {
	got := formatCommand([]string{"docker", "run", "--rm", "img", "sh", "-c", "echo $shard && cargo test"})
	assert.Equal(t, "docker run --rm img sh -c 'echo $shard && cargo test'", got)
}

func TestFormatCommandEscapesSingleQuotes(t *testing.T) {
	got := formatCommand([]string{"sh", "-c", "echo 'hi'"})
	assert.Equal(t, `sh -c 'echo '"'"'hi'"'"''`, got)
}

func TestVerboseCommandShowsShellQuoting(t *testing.T) {
	original := ExecDocker
	defer func() { ExecDocker = original }()
	ExecDocker = func(args []string) DockerResult { return DockerResult{} }

	stdout, _ := captureFD(t, func() {
		err := ExecuteStage(StageExecution{
			Name:     "test",
			Runner:   "alpine:latest",
			Commands: []string{"echo $shard", "cargo test"},
			Verbose:  true,
		}, nil, "proj")
		assert.NoError(t, err)
	})
	assert.Contains(t, stdout, "sh -c 'echo $shard && cargo test'")
}

func TestLinePrefixWriterSplitsChunks(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	w := &linePrefixWriter{w: &buf, prefix: "build", mu: &mu}
	_, err := w.Write([]byte("hel"))
	assert.NoError(t, err)
	assert.Empty(t, buf.String())
	_, err = w.Write([]byte("lo\nwor"))
	assert.NoError(t, err)
	assert.Equal(t, "build | hello\n", buf.String())
	w.Flush()
	assert.Equal(t, "build | hello\nbuild | wor\n", buf.String())
}

func TestDockerCreateArgsDropsAutoRemove(t *testing.T) {
	got := dockerCreateArgs([]string{"docker", "run", "--rm", "--init", "alpine", "echo", "hi"})
	assert.Equal(t, []string{"docker", "create", "--init", "alpine", "echo", "hi"}, got)
}

func TestRunDockerContainerKeepsDelayedOutput(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not available")
	}

	var stdout, stderr bytes.Buffer
	result := runDockerContainer(context.Background(), []string{
		"docker", "run", "--rm", "alpine:latest", "sh", "-c",
		"echo line-1; echo warn-1 >&2; sleep 1; echo line-2; echo warn-2 >&2",
	}, &stdout, &stderr)
	assert.NoError(t, result.Error)
	assert.Equal(t, 0, result.ExitCode)
	assert.Equal(t, "line-1\nline-2\n", stdout.String())
	assert.Equal(t, "warn-1\nwarn-2\n", stderr.String())
	assert.True(t, result.Streamed)
}

func TestRunDockerContainerCapturesCreateFailure(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not available")
	}

	var streamed bytes.Buffer
	result := runDockerContainer(context.Background(), []string{
		"docker", "run", "--rm", "--not-a-real-option", "alpine:latest", "echo", "hi",
	}, &streamed, &streamed)
	assert.Error(t, result.Error)
	assert.False(t, result.Streamed)
	assert.Empty(t, streamed.String())
	assert.NotEmpty(t, result.Stderr)
}

func TestRunDockerContainerExitStatus(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not available")
	}

	var stderr bytes.Buffer
	result := runDockerContainer(context.Background(), []string{
		"docker", "run", "--rm", "alpine:latest", "sh", "-c", "echo boom >&2; exit 7",
	}, io.Discard, &stderr)
	assert.Error(t, result.Error)
	assert.Equal(t, 7, result.ExitCode)
	assert.Equal(t, "boom\n", stderr.String())
}

func TestRunDockerStreamsBeforeExit(t *testing.T) {
	r, w, err := os.Pipe()
	assert.NoError(t, err)
	defer r.Close()

	started := time.Now()
	done := make(chan DockerResult, 1)
	go func() {
		done <- runDockerStreaming(context.Background(), []string{"sh", "-c", "echo hello; sleep 0.4"}, w, io.Discard)
	}()

	buf := make([]byte, 16)
	n, err := r.Read(buf)
	assert.NoError(t, err)
	assert.Equal(t, "hello\n", string(buf[:n]))
	assert.Less(t, time.Since(started), 300*time.Millisecond)

	result := <-done
	_ = w.Close()
	assert.NoError(t, result.Error)
	assert.Equal(t, "hello\n", result.Stdout)
	assert.True(t, result.Streamed)
}

func TestResolveLocalhostImage(t *testing.T) {
	assert.Equal(t, "localhost/cypherlite-sonic-rust:1.88", ResolveRunnerImage("localhost/cypherlite-sonic-rust:1.88", "public.ecr.aws"))
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
			Verbose:      true,
			PrefixOutput: true,
			OutputLock:   &mu,
		}, nil, "proj")
		assert.NoError(t, err)
	})
	assert.Contains(t, stdout, "build | hello\n")
	assert.Contains(t, stderr, "build | warn\n")
}

func TestQuietExecuteStageHidesSuccessOutput(t *testing.T) {
	originalExecDocker := ExecDocker
	defer func() { ExecDocker = originalExecDocker }()
	ExecDocker = func(args []string) DockerResult {
		return DockerResult{Stdout: "Tests passed\n"}
	}
	stdout, _ := captureFD(t, func() {
		err := ExecuteStage(StageExecution{
			Name:     "unit-test",
			Runner:   "alpine:latest",
			Commands: []string{"go test ./..."},
		}, nil, "proj")
		assert.NoError(t, err)
	})
	assert.NotContains(t, stdout, "Tests passed")
}

func TestQuietExecuteStageCapturesFailure(t *testing.T) {
	originalExecDocker := ExecDocker
	defer func() { ExecDocker = originalExecDocker }()
	ExecDocker = func(args []string) DockerResult {
		return DockerResult{Stderr: "Root dir must be an absolute path: 'docs'\n", Error: fmt.Errorf("exit status 1"), ExitCode: 1}
	}
	stdout, stderr := captureFD(t, func() {
		err := ExecuteStage(StageExecution{
			Name:     "link-check",
			Runner:   "alpine:latest",
			Commands: []string{"lychee"},
		}, nil, "proj")
		var captured *StageOutputError
		assert.Error(t, err)
		assert.ErrorAs(t, err, &captured)
		assert.Contains(t, captured.Stderr, "Root dir must be an absolute path")
	})
	assert.NotContains(t, stdout, "Root dir")
	assert.NotContains(t, stderr, "Root dir")
}

func TestEnvFileKeepsSecretsOffTheCommandLine(t *testing.T) {
	t.Setenv("SUPER_SECRET", "s3cr3t-value")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "real-secret")
	original := ExecDocker
	defer func() { ExecDocker = original }()

	var command string
	var envBody string
	ExecDocker = func(args []string) DockerResult {
		command = strings.Join(args, " ")
		for i, arg := range args {
			if arg == "--env-file" && i+1 < len(args) {
				data, err := os.ReadFile(args[i+1])
				assert.NoError(t, err)
				envBody = string(data)
				info, err := os.Stat(args[i+1])
				assert.NoError(t, err)
				assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
			}
		}
		for _, arg := range args {
			assert.NotEqual(t, "-e", arg)
		}
		return DockerResult{}
	}

	err := ExecuteStage(StageExecution{
		Name:   "deploy",
		Runner: "alpine:latest",
		Commands: []string{
			"echo ok",
		},
		Environment: map[string]string{
			"FOO":                    "AWS_SECRET_ACCESS_KEY",
			"GOSONIC_AUDIT_HMAC_KEY": "should-not-leak",
		},
		Secrets: []string{"SUPER_SECRET", "GOSONIC_AUDIT_HMAC_KEY"},
	}, nil, "proj")
	assert.NoError(t, err)
	assert.NotContains(t, command, "s3cr3t-value")
	assert.NotContains(t, command, "real-secret")
	assert.NotContains(t, command, "should-not-leak")
	assert.Contains(t, envBody, "FOO=AWS_SECRET_ACCESS_KEY")
	assert.Contains(t, envBody, "SUPER_SECRET=s3cr3t-value")
	assert.NotContains(t, envBody, "real-secret")
	assert.NotContains(t, envBody, "should-not-leak")
	assert.NotContains(t, envBody, "GOSONIC_AUDIT_HMAC_KEY")
}

func TestDockerSocketRequiresOptIn(t *testing.T) {
	original := ExecDocker
	defer func() { ExecDocker = original }()
	called := false
	ExecDocker = func(args []string) DockerResult {
		called = true
		return DockerResult{}
	}
	err := ExecuteStage(StageExecution{
		Name:         "build",
		Runner:       "alpine:latest",
		Commands:     []string{"echo hi"},
		DockerSocket: true,
	}, nil, "proj")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "docker socket is not allowed")
	assert.False(t, called)
}

func TestPublishedPortBindsLoopback(t *testing.T) {
	assert.Equal(t, "127.0.0.1:5432:5432", publishedPort("5432:5432"))
	assert.Equal(t, "127.0.0.1:5432", publishedPort("5432"))
	assert.Equal(t, "127.0.0.1:5432:5432", publishedPort("127.0.0.1:5432:5432"))
	assert.Equal(t, "0.0.0.0:80:80", publishedPort("0.0.0.0:80:80"))
}

func TestMountOfAuditKeyIsRefused(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "audit.key")
	assert.NoError(t, os.WriteFile(keyPath, bytes.Repeat([]byte{9}, 32), 0600))
	t.Setenv("GOSONIC_AUDIT_HMAC_KEY", "")
	t.Setenv("GOSONIC_AUDIT_HMAC_KEY_FILE", keyPath)

	original := ExecDocker
	defer func() { ExecDocker = original }()
	ExecDocker = func(args []string) DockerResult { return DockerResult{} }

	err := ExecuteStage(StageExecution{
		Name:     "build",
		Runner:   "alpine:latest",
		Commands: []string{"echo hi"},
		Volumes:  []Volume{{Type: "bind", Source: dir, Target: "/keys"}},
	}, nil, "proj")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "audit signing key")

	assert.False(t, mountsAuditKey(".", keyPath))
}

func TestRegistryAllowlist(t *testing.T) {
	assert.True(t, RegistryAllowed("public.ecr.aws", nil))
	assert.True(t, RegistryAllowed("localhost:5000", nil))
	assert.True(t, RegistryAllowed("evil.example", []string{"evil.example"}))
	assert.False(t, RegistryAllowed("evil.example", nil))

	err := ValidateImage("evil.example/app:latest", nil, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not allowed")

	err = ValidateImage("public.ecr.aws/docker/library/alpine:latest", nil, true)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "digest")

	assert.NoError(t, ValidateImage("public.ecr.aws/docker/library/alpine@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil, true))
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

func TestHostUserGetsTmpHome(t *testing.T) {
	path, err := writeEnvFile(stageEnvironment(StageExecution{
		Environment: map[string]string{"FOO": "bar"},
	}), nil, nil)
	assert.NoError(t, err)
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	assert.NoError(t, err)
	assert.Equal(t, "FOO=bar\nHOME=/tmp\n", string(data))
}

func TestHostUserWithNoEnvironmentStillGetsHome(t *testing.T) {
	path, err := writeEnvFile(stageEnvironment(StageExecution{}), nil, nil)
	assert.NoError(t, err)
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	assert.NoError(t, err)
	assert.Equal(t, "HOME=/tmp\n", string(data))
}

func TestExplicitHomeWinsOverDefault(t *testing.T) {
	path, err := writeEnvFile(stageEnvironment(StageExecution{
		Environment: map[string]string{"HOME": "/root"},
	}), nil, nil)
	assert.NoError(t, err)
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	assert.NoError(t, err)
	assert.Equal(t, "HOME=/root\n", string(data))
}

func TestNamedUserDoesNotGetDefaultHome(t *testing.T) {
	path, err := writeEnvFile(stageEnvironment(StageExecution{
		User:        "root",
		Environment: map[string]string{"FOO": "bar"},
	}), nil, nil)
	assert.NoError(t, err)
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	assert.NoError(t, err)
	assert.Equal(t, "FOO=bar\n", string(data))
}

func TestEnvFileValuesAreLiteral(t *testing.T) {
	path, err := writeEnvFile(map[string]string{
		"SPACED":  "a b",
		"EMPTY":   "",
		"QUOTE":   `q"uote`,
		"BACKSLH": `back\slash`,
		"HASH":    "x#y",
	}, nil, nil)
	assert.NoError(t, err)
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	assert.NoError(t, err)
	assert.Equal(t, "BACKSLH=back\\slash\nEMPTY=\nHASH=x#y\nQUOTE=q\"uote\nSPACED=a b\n", string(data))
}

func TestEnvFileRejectsMultilineValue(t *testing.T) {
	_, err := writeEnvFile(map[string]string{"KEY": "one\ntwo"}, nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "spans lines")
}

func TestOptionalSecretsAreEmptyWhenUnset(t *testing.T) {
	t.Setenv("OPT_SET", "value")
	t.Setenv("OPT_EMPTY", "")
	os.Unsetenv("OPT_UNSET")

	path, err := writeEnvFile(nil, nil, []string{"OPT_SET", "OPT_EMPTY", "OPT_UNSET"})
	assert.NoError(t, err)
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	assert.NoError(t, err)
	assert.Equal(t, "OPT_EMPTY=\nOPT_SET=value\nOPT_UNSET=\n", string(data))
}

func TestRequiredSecretStillFailsWhenUnset(t *testing.T) {
	os.Unsetenv("REQ_UNSET")
	_, err := writeEnvFile(nil, []string{"REQ_UNSET"}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not set")
}

func TestOptionalSecretsCannotLeakAuditKey(t *testing.T) {
	t.Setenv("GOSONIC_AUDIT_HMAC_KEY", "should-not-leak")
	path, err := writeEnvFile(nil, nil, []string{"GOSONIC_AUDIT_HMAC_KEY"})
	assert.NoError(t, err)
	if path == "" {
		return // the protected name was dropped and nothing else was set
	}
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	assert.NoError(t, err)
	assert.NotContains(t, string(data), "should-not-leak")
}
