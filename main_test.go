package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gosonic/lib"

	"flag"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/urfave/cli/v2"
)

type MockAuditStore struct {
	mock.Mock
}

func (m *MockAuditStore) Store(log lib.AuditLog) error {
	args := m.Called(log)
	return args.Error(0)
}

func (m *MockAuditStore) LoadLogs(project, gitRevision string) ([]lib.AuditLog, error) {
	args := m.Called(project, gitRevision)
	return args.Get(0).([]lib.AuditLog), args.Error(1)
}

func TestLoadConfig(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".sonic.yml")

	configData := []byte(`
version: "1"
project:
  name: "test-project"
  language: "go"
  root: "."
stages:
  build:
    runner: "golang"
    version: "1.22"
    commands:
      - "go build ./..."
    volumes:
      - type: bind
        source: "."
        target: "/workspace"
    environment:
      GO111MODULE: "on"
      CGO_ENABLED: "0"
  unit-test:
    runner: "golang"
    commands:
      - "go test ./..."
    volumes:
      - type: bind
        source: "."
        target: "/workspace"
      - type: bind
        source: "${HOME}/.cache/go-build"
        target: "/root/.cache/go-build"
    environment:
      GO111MODULE: "on"
      TEST_MODE: "unit"
`)

	err := os.WriteFile(configPath, configData, 0644)
	assert.NoError(t, err)

	config, err := loadConfig(configPath, nil)
	assert.NoError(t, err)
	assert.Equal(t, "1", config.Version)
	assert.Equal(t, "test-project", config.Project.Name)
	assert.Contains(t, config.Stages, "build")
	assert.Contains(t, config.Stages, "unit-test")

	// Test with variables
	vars := execVars{
		"HOME": "/home/user",
	}
	config, err = loadConfig(configPath, vars)
	assert.NoError(t, err)
	unitTest := config.Stages["unit-test"]
	assert.Equal(t, "/home/user/.cache/go-build", unitTest.Volumes[1].Source)
}

func TestResolveConfigPath(t *testing.T) {
	wd, err := os.Getwd()
	assert.NoError(t, err)
	dir := t.TempDir()
	assert.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(wd) })

	got, err := resolveConfigPath("")
	assert.NoError(t, err)
	assert.Equal(t, defaultConfigFile, got)
	got, err = resolveConfigPath("custom.yml")
	assert.NoError(t, err)
	assert.Equal(t, "custom.yml", got)

	assert.NoError(t, os.WriteFile(legacyConfigFile, []byte("legacy: true"), 0644))
	got, err = resolveConfigPath("")
	assert.NoError(t, err)
	assert.Equal(t, legacyConfigFile, got)

	assert.NoError(t, os.MkdirAll(".sonic", 0755))
	assert.NoError(t, os.WriteFile(defaultConfigFile, []byte("nested: true"), 0644))
	got, err = resolveConfigPath("")
	assert.NoError(t, err)
	assert.Equal(t, defaultConfigFile, got)
}

func TestResolveConfigPathChoosesTheOnlyWorkflow(t *testing.T) {
	wd, err := os.Getwd()
	assert.NoError(t, err)
	assert.NoError(t, os.Chdir(t.TempDir()))
	t.Cleanup(func() { _ = os.Chdir(wd) })

	assert.NoError(t, os.MkdirAll(".sonic", 0755))
	assert.NoError(t, os.WriteFile(filepath.Join(".sonic", "ci.yml"), []byte("ci: true"), 0644))
	got, err := resolveConfigPath("")
	assert.NoError(t, err)
	assert.Equal(t, filepath.Join(".sonic", "ci.yml"), got)

	assert.NoError(t, os.WriteFile(filepath.Join(".sonic", "release.yml"), []byte("release: true"), 0644))
	_, err = resolveConfigPath("")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "several workflows")
	assert.Contains(t, err.Error(), "\n  ci\n")
	assert.Contains(t, err.Error(), "\n  release")

	assert.NoError(t, os.WriteFile(defaultConfigFile, []byte("nested: true"), 0644))
	got, err = resolveConfigPath("")
	assert.NoError(t, err)
	assert.Equal(t, defaultConfigFile, got)
}

func TestWorkflowCommand(t *testing.T) {
	wd, err := os.Getwd()
	assert.NoError(t, err)
	assert.NoError(t, os.Chdir(t.TempDir()))
	t.Cleanup(func() { _ = os.Chdir(wd) })
	assert.NoError(t, os.MkdirAll(".sonic", 0755))
	ciYAML := []byte(`
version: "1"
project:
  name: demo
stages:
  lint:
    runner: alpine
    commands: ["echo from-ci"]
suites:
  smoke:
    - lint
`)
	releaseYAML := []byte(`
version: "1"
project:
  name: demo
stages:
  ship:
    runner: alpine
    commands: ["echo from-release"]
`)
	assert.NoError(t, os.WriteFile(filepath.Join(".sonic", "ci.yml"), ciYAML, 0644))
	assert.NoError(t, os.WriteFile(filepath.Join(".sonic", "release.yml"), releaseYAML, 0644))

	inv, err := prepareInvocation([]string{"gosonic", "--verbose", "ci", "run", "--jobs", "4", "lint"})
	assert.NoError(t, err)
	assert.Equal(t, "ci", inv.workflow)
	assert.Equal(t, filepath.Join(".sonic", "ci.yml"), inv.configPath)
	assert.Equal(t, []string{"gosonic", "--verbose", "run", "--jobs", "4", "lint"}, inv.args)

	other := filepath.Join(t.TempDir(), "other.yml")
	assert.NoError(t, os.WriteFile(other, []byte(`
version: "1"
project:
  name: other
stages:
  lint:
    runner: alpine
    commands: ["echo from-file"]
`), 0644))
	inv, err = prepareInvocation([]string{"gosonic", "--sonic-file", other, "ci", "run", "lint"})
	assert.NoError(t, err)
	assert.Empty(t, inv.workflow)
	assert.Equal(t, other, inv.configPath)
	assert.Equal(t, []string{"gosonic", "--sonic-file", other, "ci", "run", "lint"}, inv.args)

	original := lib.ExecDocker
	t.Cleanup(func() { lib.ExecDocker = original })
	var mu sync.Mutex
	var ran []string
	lib.ExecDocker = func(args []string) lib.DockerResult {
		joined := strings.Join(args, " ")
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(joined, "from-ci"):
			ran = append(ran, "ci")
		case strings.Contains(joined, "from-release"):
			ran = append(ran, "release")
		case strings.Contains(joined, "from-file"):
			ran = append(ran, "file")
		}
		return lib.DockerResult{}
	}
	t.Setenv("GO_TEST", "1")

	_, stderr, err := captureOutput(func() error {
		return run([]string{"gosonic", "ci", "run", "lint"})
	})
	assert.NoError(t, err, stderr)
	assert.Equal(t, []string{"ci"}, ran)

	_, stderr, err = captureOutput(func() error {
		return run([]string{"gosonic", "nightly", "run", "lint"})
	})
	assert.Error(t, err)
	assert.Contains(t, stderr, `unknown workflow "nightly"`)
	assert.Contains(t, stderr, "\n  ci\n")
	assert.Contains(t, stderr, "\n  release")

	_, stderr, err = captureOutput(func() error {
		return run([]string{"gosonic", "ci", "lint"})
	})
	assert.Error(t, err)
	assert.Contains(t, stderr, "did you mean: gosonic ci run lint")

	_, stderr, err = captureOutput(func() error {
		return run([]string{"gosonic", "ci"})
	})
	assert.Error(t, err)
	assert.Contains(t, stderr, "did you mean: gosonic ci run\n")

	_, stderr, err = captureOutput(func() error {
		return run([]string{"gosonic", "ci.yml", "run", "lint"})
	})
	assert.Error(t, err)
	assert.Contains(t, stderr, `invalid workflow name "ci.yml"`)

	helpOut, _, helpErr := captureOutput(func() error {
		return run([]string{"gosonic", "ci", "run", "--help"})
	})
	assert.NoError(t, helpErr)
	assert.Contains(t, helpOut, "workflow ci (.sonic/ci.yml)")
	assert.Contains(t, helpOut, "smoke")
	assert.Contains(t, helpOut, "lint")

	_, stderr, err = captureOutput(func() error {
		return run([]string{"gosonic", "ci", "run", "missing"})
	})
	assert.Error(t, err)
	assert.Contains(t, stderr, "workflow ci (.sonic/ci.yml)")
	assert.Contains(t, stderr, "invalid stage(s): missing")

	_, stderr, err = captureOutput(func() error {
		return run([]string{"gosonic", "run", "lint"})
	})
	assert.Error(t, err)
	assert.Contains(t, stderr, "several workflows in .sonic/; name one before run:")
	assert.Contains(t, stderr, "\n  ci\n")
	assert.Contains(t, stderr, "\n  release")

	assert.NoError(t, os.Remove(filepath.Join(".sonic", "release.yml")))
	mu.Lock()
	ran = nil
	mu.Unlock()
	_, stderr, err = captureOutput(func() error {
		return run([]string{"gosonic", "run", "lint"})
	})
	assert.NoError(t, err, stderr)
	assert.Equal(t, []string{"ci"}, ran)

	mu.Lock()
	ran = nil
	mu.Unlock()
	_, stderr, err = captureOutput(func() error {
		return run([]string{"gosonic", "--sonic-file", other, "run", "lint"})
	})
	assert.NoError(t, err, stderr)
	assert.Equal(t, []string{"file"}, ran)
}

func TestRunnerAliases(t *testing.T) {
	const image = "docker/library/golang:1.20-alpine3.17"

	tests := map[string]struct {
		yaml    string
		vars    execVars
		want    map[string]string
		wantErr string
	}{
		"list of aliases": {
			yaml: `
version: "1"
runners:
  - alpine: "docker/library/golang:1.20-alpine3.17"
stages:
  test:
    runner: alpine
  build:
    runner: "docker/library/golang:1.22"
`,
			want: map[string]string{
				"test":  image,
				"build": "docker/library/golang:1.22",
			},
		},
		"mapping of aliases": {
			yaml: `
version: "1"
runners:
  alpine: "docker/library/golang:1.20-alpine3.17"
stages:
  test:
    runner: alpine
`,
			want: map[string]string{"test": image},
		},
		"alias image can use variables": {
			yaml: `
version: "1"
runners:
  - alpine: "${REGISTRY}/golang:1.20-alpine3.17"
stages:
  test:
    runner: alpine
`,
			vars: execVars{"REGISTRY": "docker/library"},
			want: map[string]string{"test": image},
		},
		"duplicate alias": {
			yaml: `
version: "1"
runners:
  - alpine: "docker/library/golang:1.20-alpine3.17"
  - alpine: "docker/library/golang:1.22"
stages:
  test:
    runner: alpine
`,
			wantErr: `duplicate runner alias "alpine"`,
		},
		"empty image": {
			yaml: `
version: "1"
runners:
  - alpine: ""
stages:
  test:
    runner: alpine
`,
			wantErr: `runner "alpine" has an empty image`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".sonic.yml")
			err := os.WriteFile(path, []byte(tc.yaml), 0644)
			assert.NoError(t, err)

			config, err := loadConfig(path, tc.vars)
			if tc.wantErr != "" {
				assert.ErrorContains(t, err, tc.wantErr)
				return
			}
			assert.NoError(t, err)
			for stage, runner := range tc.want {
				assert.Equal(t, runner, config.Stages[stage].Runner)
			}
		})
	}
}

func captureOutput(f func() error) (stdout string, stderr string, err error) {
	oldStdout := os.Stdout
	oldStderr := os.Stderr

	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()

	os.Stdout = wOut
	os.Stderr = wErr

	outC := make(chan string)
	errC := make(chan string)

	// Copy the output in a separate goroutine so printing can't block indefinitely
	go func() {
		var outBuf bytes.Buffer
		io.Copy(&outBuf, rOut)
		outC <- outBuf.String()
	}()

	go func() {
		var errBuf bytes.Buffer
		io.Copy(&errBuf, rErr)
		errC <- errBuf.String()
	}()

	// Run the function
	err = f()

	// Close the pipes
	wOut.Close()
	wErr.Close()

	// Restore original stdout/stderr
	os.Stdout = oldStdout
	os.Stderr = oldStderr

	// Collect output
	stdout = <-outC
	stderr = <-errC

	return
}

func TestStageExecution(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-sonic.yml")

	configData := []byte(`
version: "1"
project:
  name: "test-project"
  language: "go"
  root: "."
stages:
  unit-test:
    runner: "golang"
    commands:
      - "go test ./..."
  build:
    runner: "golang"
    commands:
      - "go build ./..."
  deploy:
    runner: "kubernetes"
    commands:
      - "kubectl apply -f k8s/"
`)

	err := os.WriteFile(configPath, configData, 0644)
	assert.NoError(t, err)

	// Mock docker execution
	mockResults := map[string]lib.DockerResult{
		"unit-test": {Stdout: "Tests passed\n"},
		"build":     {Stdout: "Build successful\n"},
		"deploy":    {Stdout: "Deployment complete\n"},
	}

	lib.ExecDocker = func(args []string) lib.DockerResult {
		// Find which command is being executed
		var cmdType string

		// Check for direct command execution (no shell)
		cmdStr := strings.Join(args, " ")
		if strings.Contains(cmdStr, "go test") {
			cmdType = "unit-test"
		} else if strings.Contains(cmdStr, "go build") {
			cmdType = "build"
		} else if strings.Contains(cmdStr, "kubectl apply") {
			cmdType = "deploy"
		} else {
			// Check for shell-wrapped commands
			for i, arg := range args {
				if arg == "sh" && i+2 < len(args) && args[i+1] == "-c" {
					switch {
					case strings.Contains(args[i+2], "go test"):
						cmdType = "unit-test"
					case strings.Contains(args[i+2], "go build"):
						cmdType = "build"
					case strings.Contains(args[i+2], "kubectl"):
						cmdType = "deploy"
					}
					break
				}
			}
		}

		if result, ok := mockResults[cmdType]; ok {
			return result
		}
		return lib.DockerResult{
			Stderr:   "Unknown command",
			Error:    fmt.Errorf("unknown command"),
			ExitCode: 1,
		}
	}

	tests := map[string]struct {
		args         []string
		wantStdout   []string
		wantStderr   []string
		forbidStdout []string
		wantErr      bool
	}{
		"run all stages in order": {
			args: []string{"gosonic", "--sonic-file", configPath, "run", "unit-test", "build", "deploy"},
			wantStdout: []string{
				"unit-test",
				"build",
				"deploy",
				"ok",
			},
			// Quiet mode hides container output. Sequential runs are unprefixed.
			forbidStdout: []string{
				"Tests passed",
				"Build successful",
				"Deployment complete",
				"unit-test | ",
				"build | ",
				"deploy | ",
				"\r",
			},
		},
		"verbose still streams command output": {
			args: []string{"gosonic", "--sonic-file", configPath, "--verbose", "run", "unit-test"},
			wantStdout: []string{
				"Tests passed",
				"Docker command:",
			},
		},
		"jobs must be at least 1": {
			args:    []string{"gosonic", "--sonic-file", configPath, "run", "--jobs", "0", "unit-test"},
			wantErr: true,
			wantStderr: []string{
				"--jobs must be at least 1",
			},
		},
		"invalid stage": {
			args:    []string{"gosonic", "--sonic-file", configPath, "run", "invalid"},
			wantErr: true,
			wantStderr: []string{
				"Error: invalid stage(s): invalid",
				"Available stages:",
				"  unit-test - golang",
				"  build - golang",
				"  deploy - kubernetes",
			},
		},
	}

	originalGoTest := os.Getenv("GO_TEST")

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr string
			var err error
			os.Setenv("GO_TEST", "1")
			// Capture output and error
			stdout, stderr, err = captureOutput(func() error {
				return run(tc.args)
			})

			t.Logf("Stdout:\n%s", stdout)
			t.Logf("Stderr:\n%s", stderr)
			t.Logf("Expected stdout to contain: %v", tc.wantStdout)
			t.Logf("Expected stderr to contain: %v", tc.wantStderr)
			t.Logf("Expected error: %v", tc.wantErr)
			t.Logf("Got error: %v", err)

			if tc.wantErr {
				assert.Error(t, err)
				for _, want := range tc.wantStderr {
					if !strings.Contains(stderr, want) {
						t.Errorf("Expected stderr to contain %q but got:\n%s", want, stderr)
					}
				}
			} else {
				assert.NoError(t, err)
				for _, want := range tc.wantStdout {
					if !strings.Contains(stdout, want) {
						t.Errorf("Expected stdout to contain %q but got:\n%s", want, stdout)
					}
				}
				for _, forbidden := range tc.forbidStdout {
					if strings.Contains(stdout, forbidden) {
						t.Errorf("Expected stdout not to contain %q but got:\n%s", forbidden, stdout)
					}
				}
				for _, want := range tc.wantStderr {
					if !strings.Contains(stderr, want) {
						t.Errorf("Expected stderr to contain %q but got:\n%s", want, stderr)
					}
				}
			}
		})

		defer os.Setenv("GO_TEST", originalGoTest)
	}
}

func TestQuietRunDumpsFailureLog(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-sonic.yml")
	assert.NoError(t, os.WriteFile(configPath, []byte(`
version: "1"
project:
  name: "quiet-fail"
stages:
  link-check:
    runner: "alpine"
    commands:
      - "lychee"
`), 0644))

	originalExec := lib.ExecDocker
	defer func() { lib.ExecDocker = originalExec }()
	lib.ExecDocker = func(args []string) lib.DockerResult {
		return lib.DockerResult{
			Stderr:   "Root dir must be an absolute path: 'docs'\n",
			Error:    fmt.Errorf("exit status 1"),
			ExitCode: 1,
		}
	}

	oldGoTest := os.Getenv("GO_TEST")
	os.Setenv("GO_TEST", "1")
	defer os.Setenv("GO_TEST", oldGoTest)

	stdout, stderr, err := captureOutput(func() error {
		return run([]string{"gosonic", "--sonic-file", configPath, "run", "link-check"})
	})
	assert.Error(t, err)
	assert.Contains(t, stdout, "link-check")
	assert.Contains(t, stdout, "failed")
	assert.Regexp(t, `(?m)^run\s+failed\s+\d+\.\d+s$`, stdout)
	assert.NotContains(t, stdout, "\r")
	assert.Contains(t, stderr, "Root dir must be an absolute path: 'docs'")
	assert.Contains(t, stderr, `Error: stage "link-check" failed`)
}

func TestParallelRunSchedulesRequiresAndPrefixesOutput(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-sonic.yml")
	auditPath := filepath.Join(tmpDir, "logs")
	configData := []byte(`
version: "1"
project:
  name: "parallel-project"
stages:
  lint:
    runner: "golang"
    commands:
      - "echo lint"
  unit-test:
    runner: "golang"
    commands:
      - "go test ./..."
  build:
    runner: "golang"
    requires: ["unit-test"]
    commands:
      - "go build ./..."
`)
	err := os.WriteFile(configPath, configData, 0644)
	assert.NoError(t, err)

	originalExec := lib.ExecDocker
	defer func() { lib.ExecDocker = originalExec }()

	var mu sync.Mutex
	var ran []string
	lib.ExecDocker = func(args []string) lib.DockerResult {
		joined := strings.Join(args, " ")
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(joined, "echo lint"):
			ran = append(ran, "lint")
			return lib.DockerResult{Stdout: "lint ok\n"}
		case strings.Contains(joined, "go test"):
			ran = append(ran, "unit-test")
			return lib.DockerResult{Stdout: "Tests passed\n"}
		case strings.Contains(joined, "go build"):
			ran = append(ran, "build")
			return lib.DockerResult{Stdout: "Build successful\n"}
		default:
			return lib.DockerResult{Error: fmt.Errorf("unexpected command %s", joined), ExitCode: 1}
		}
	}

	oldGoTest := os.Getenv("GO_TEST")
	os.Setenv("GO_TEST", "1")
	defer os.Setenv("GO_TEST", oldGoTest)

	stdout, stderr, err := captureOutput(func() error {
		return run([]string{
			"gosonic", "--sonic-file", configPath, "--audit-path", auditPath,
			"run", "--jobs", "4", "lint", "unit-test", "build",
		})
	})
	assert.NoError(t, err, stderr)
	assert.Regexp(t, `(?m)^lint\s+\| ok\s+\d+\.\d+s$`, stdout)
	assert.Regexp(t, `(?m)^unit-test \| ok\s+\d+\.\d+s$`, stdout)
	assert.Regexp(t, `(?m)^build\s+\| ok\s+\d+\.\d+s$`, stdout)
	assert.Regexp(t, `(?m)^run\s+\| ok\s+\d+\.\d+s$`, stdout)
	assert.NotContains(t, stdout, "lint ok")
	assert.NotContains(t, stdout, "Tests passed")
	assert.NotContains(t, stdout, "\r")

	mu.Lock()
	defer mu.Unlock()
	unitTestAt := strings.Index(strings.Join(ran, ","), "unit-test")
	buildAt := strings.Index(strings.Join(ran, ","), "build")
	assert.GreaterOrEqual(t, unitTestAt, 0)
	assert.Greater(t, buildAt, unitTestAt)
}

func TestParallelRunRejectsUnmetExternalRequirement(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-sonic.yml")
	auditPath := filepath.Join(tmpDir, "logs")
	configData := []byte(`
version: "1"
project:
  name: "parallel-project"
stages:
  lint:
    runner: "golang"
    commands:
      - "echo lint"
  build:
    runner: "golang"
    requires: ["test"]
    commands:
      - "go build ./..."
`)
	err := os.WriteFile(configPath, configData, 0644)
	assert.NoError(t, err)

	originalExec := lib.ExecDocker
	defer func() { lib.ExecDocker = originalExec }()
	calls := 0
	lib.ExecDocker = func(args []string) lib.DockerResult {
		calls++
		return lib.DockerResult{Stdout: "should not run\n"}
	}

	oldGoTest := os.Getenv("GO_TEST")
	os.Setenv("GO_TEST", "1")
	defer os.Setenv("GO_TEST", oldGoTest)

	_, stderr, err := captureOutput(func() error {
		return run([]string{
			"gosonic", "--sonic-file", configPath, "--audit-path", auditPath,
			"run", "--jobs", "2", "lint", "build",
		})
	})
	assert.Error(t, err)
	assert.Contains(t, stderr, "requirements not met")
	assert.Contains(t, stderr, "test")
	assert.Equal(t, 0, calls)
}

func TestCreateAuditStore(t *testing.T) {
	tmpDir := t.TempDir()

	// Mock S3 client creation
	originalCreateS3Client := createS3Client
	createS3Client = func(verbose bool) (lib.S3Client, error) {
		mockClient := new(lib.MockS3Client)
		mockClient.On("PutObject", mock.Anything, mock.Anything).Return(&s3.PutObjectOutput{}, nil)
		mockClient.On("ListObjectsV2", mock.Anything, mock.Anything).Return(&s3.ListObjectsV2Output{}, nil)
		return mockClient, nil
	}
	defer func() { createS3Client = originalCreateS3Client }()

	tests := map[string]struct {
		config      *Config
		flags       map[string]string
		env         map[string]string
		wantType    string
		wantPath    string
		wantBucket  string
		wantErr     bool
		wantErrText string
	}{
		"default config": {
			config:   &Config{},
			wantType: "file",
			wantPath: ".logs",
		},
		"file store from config": {
			config: &Config{
				Audit: struct {
					Store      string `yaml:"store"`
					Path       string `yaml:"path"`
					S3Bucket   string `yaml:"s3bucket"`
					AWSProfile string `yaml:"aws_profile"`
				}{
					Store: "file",
					Path:  filepath.Join(tmpDir, "audit-logs"),
				},
			},
			wantType: "file",
			wantPath: filepath.Join(tmpDir, "audit-logs"),
		},
		"s3 store from config": {
			config: &Config{
				Audit: struct {
					Store      string `yaml:"store"`
					Path       string `yaml:"path"`
					S3Bucket   string `yaml:"s3bucket"`
					AWSProfile string `yaml:"aws_profile"`
				}{
					Store:    "s3",
					Path:     "logs/prefix",
					S3Bucket: "my-bucket",
				},
			},
			wantType:   "s3",
			wantPath:   "logs/prefix",
			wantBucket: "my-bucket",
		},
		"cli flags override config": {
			config: &Config{
				Audit: struct {
					Store      string `yaml:"store"`
					Path       string `yaml:"path"`
					S3Bucket   string `yaml:"s3bucket"`
					AWSProfile string `yaml:"aws_profile"`
				}{
					Store:    "file",
					Path:     "config-logs",
					S3Bucket: "config-bucket",
				},
			},
			flags: map[string]string{
				"--audit-store":     "s3",
				"--audit-path":      "cli-logs",
				"--audit-s3-bucket": "cli-bucket",
			},
			wantType:   "s3",
			wantPath:   "cli-logs",
			wantBucket: "cli-bucket",
		},
		"env vars override config": {
			config: &Config{
				Audit: struct {
					Store      string `yaml:"store"`
					Path       string `yaml:"path"`
					S3Bucket   string `yaml:"s3bucket"`
					AWSProfile string `yaml:"aws_profile"`
				}{
					Store:    "file",
					Path:     "config-logs",
					S3Bucket: "config-bucket",
				},
			},
			env: map[string]string{
				"SONIC_AUDIT_STORE":     "s3",
				"SONIC_AUDIT_PATH":      "env-logs",
				"SONIC_AUDIT_S3_BUCKET": "env-bucket",
			},
			wantType:   "s3",
			wantPath:   "env-logs",
			wantBucket: "env-bucket",
		},
		"s3 store without bucket": {
			config: &Config{
				Audit: struct {
					Store      string `yaml:"store"`
					Path       string `yaml:"path"`
					S3Bucket   string `yaml:"s3bucket"`
					AWSProfile string `yaml:"aws_profile"`
				}{
					Store: "s3",
					Path:  "logs",
				},
			},
			wantErr:     true,
			wantErrText: "s3 bucket must be specified for s3 audit store",
		},
		"invalid store type": {
			config: &Config{
				Audit: struct {
					Store      string `yaml:"store"`
					Path       string `yaml:"path"`
					S3Bucket   string `yaml:"s3bucket"`
					AWSProfile string `yaml:"aws_profile"`
				}{
					Store: "invalid",
				},
			},
			wantErr:     true,
			wantErrText: "unknown audit store type: invalid",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			// Set up environment
			for k, v := range tc.env {
				os.Setenv(k, v)
			}
			defer func() {
				for k := range tc.env {
					os.Unsetenv(k)
				}
			}()

			// Create CLI context
			app := cli.NewApp()
			app.Flags = []cli.Flag{
				&cli.StringFlag{
					Name:    "audit-store",
					Usage:   "Audit log storage type (file or s3)",
					EnvVars: []string{"SONIC_AUDIT_STORE"},
				},
				&cli.StringFlag{
					Name:    "audit-path",
					Usage:   "Path for audit logs (directory for file store, prefix for S3)",
					EnvVars: []string{"SONIC_AUDIT_PATH"},
				},
				&cli.StringFlag{
					Name:    "audit-s3-bucket",
					Usage:   "S3 bucket name for audit logs when using s3 store",
					EnvVars: []string{"SONIC_AUDIT_S3_BUCKET"},
				},
			}

			// Create a new flag set and parse the flags
			set := flag.NewFlagSet("test", flag.ContinueOnError)
			for _, f := range app.Flags {
				f.Apply(set)
			}

			if len(tc.flags) > 0 {
				// Convert map to slice of strings in "key=value" format
				var flagSlice []string
				for k, v := range tc.flags {
					flagSlice = append(flagSlice, k, v)
				}
				err := set.Parse(flagSlice)
				assert.NoError(t, err)
			}

			ctx := cli.NewContext(app, set, nil)

			// Test store creation
			store, err := createAuditStore(tc.config, ctx)
			if tc.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrText)
				return
			}
			assert.NoError(t, err)

			// Check store type and configuration
			switch tc.wantType {
			case "file":
				fileStore, ok := store.(*lib.FileStore)
				assert.True(t, ok)
				assert.Equal(t, tc.wantPath, fileStore.Directory)
			case "s3":
				s3Store, ok := store.(*lib.S3Store)
				assert.True(t, ok)
				assert.Equal(t, tc.wantPath, s3Store.Prefix)
				assert.Equal(t, tc.wantBucket, s3Store.BucketName)
			}
		})
	}
}

func TestHelpCommand(t *testing.T) {
	// Set GO_TEST environment variable
	oldGoTest := os.Getenv("GO_TEST")
	os.Setenv("GO_TEST", "1")
	defer func() { os.Setenv("GO_TEST", oldGoTest) }()

	// Create a test config file with stages
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-sonic.yml")

	configData := []byte(`
version: "1"
project:
  name: "test-project"
  language: "go"
  root: "."
actions:
  setup:
    commands:
      - "echo setup"
stages:
  unit-test:
    runner: "golang"
    commands:
      - "go test ./..."
  build:
    runner: "golang"
    commands:
      - "go build ./..."
  deploy:
    runner: "kubernetes"
    requires: ["build"]
    before:
      - setup
    commands:
      - "kubectl apply -f k8s/"
`)

	err := os.WriteFile(configPath, configData, 0644)
	assert.NoError(t, err)

	// Capture help output
	stdout, stderr, err := captureOutput(func() error {
		return run([]string{"gosonic", "--sonic-file", configPath, "help"})
	})

	// Verify no errors
	assert.NoError(t, err)
	assert.Empty(t, stderr)

	// Verify global flags are present
	expectedFlags := []string{
		"--sonic-file value, -f value",
		`default: ".sonic/sonic.yml"`,
		"--audit-store value",
		"--audit-path value",
		"--audit-s3-bucket value",
	}

	// Verify built-in commands are present
	expectedCommands := []string{
		"help, h",
		"run",
	}

	// Verify dynamic stage commands are present
	expectedStages := []string{
		"unit-test",
		"build",
		"deploy",
	}

	// Log the full output for debugging
	t.Logf("Help command output:\n%s", stdout)

	// Check for global flags
	for _, flag := range expectedFlags {
		assert.Contains(t, stdout, flag, "Help output should contain global flag %q", flag)
	}

	// Check for built-in commands
	for _, cmd := range expectedCommands {
		assert.Contains(t, stdout, cmd, "Help output should contain command %q", cmd)
	}

	// Check for stage commands
	for _, stage := range expectedStages {
		assert.Contains(t, stdout, stage, "Help output should contain stage command %q", stage)
		assert.Contains(t, stdout, fmt.Sprintf("Run the %s stage", stage), "Help output should contain stage description")
	}

	buildAt := strings.Index(stdout, "\n   build")
	deployAt := strings.Index(stdout, "\n   deploy")
	assert.GreaterOrEqual(t, buildAt, 0)
	assert.Greater(t, deployAt, buildAt)
	assert.Contains(t, stdout, "Run the deploy stage; requires build; before setup")

	runHelp, _, runErr := captureOutput(func() error {
		return run([]string{"gosonic", "--sonic-file", configPath, "run", "--help"})
	})
	assert.NoError(t, runErr)
	assert.Contains(t, runHelp, "Stages, dependencies first:")
	assert.Less(t, strings.Index(runHelp, "build"), strings.Index(runHelp, "deploy"))
	assert.Contains(t, runHelp, "requires: build")
	assert.Contains(t, runHelp, "before: setup")

	// Verify usage information
	assert.Contains(t, stdout, "NAME:")
	assert.Contains(t, stdout, "gosonic - Run stages from a workflow file")
	assert.Contains(t, stdout, "gosonic [workflow] run [stages|suites]")
	assert.Contains(t, stdout, "gosonic ci run --jobs 4 full-test")
	assert.Contains(t, stdout, ".sonic/<name>.yml")
	assert.Contains(t, stdout, "USAGE:")
	assert.Contains(t, stdout, "COMMANDS:")
	assert.Contains(t, stdout, "GLOBAL OPTIONS:")
}

func TestExecVars(t *testing.T) {
	// Test parseExecVars
	tests := map[string]struct {
		input    []string
		expected execVars
	}{
		"empty": {
			input:    []string{},
			expected: execVars{},
		},
		"single var": {
			input: []string{"region.name=us-east-1"},
			expected: execVars{
				"region.name": "us-east-1",
			},
		},
		"multiple vars": {
			input: []string{
				"region.name=us-east-1",
				"env=prod",
				"version=1.2.3",
			},
			expected: execVars{
				"region.name": "us-east-1",
				"env":         "prod",
				"version":     "1.2.3",
			},
		},
		"invalid format": {
			input: []string{
				"region.name=us-east-1",
				"invalid",
				"also-invalid=",
			},
			expected: execVars{
				"region.name": "us-east-1",
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			result := parseExecVars(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestResolveVars(t *testing.T) {
	vars := execVars{
		"region.name": "us-east-1",
		"env":         "prod",
		"version":     "1.2.3",
	}

	tests := map[string]struct {
		input    string
		expected string
	}{
		"no variables": {
			input:    "plain text",
			expected: "plain text",
		},
		"single variable": {
			input:    "region: ${region.name}",
			expected: "region: us-east-1",
		},
		"multiple variables": {
			input:    "deploy to ${region.name} in ${env} with v${version}",
			expected: "deploy to us-east-1 in prod with v1.2.3",
		},
		"undefined variable": {
			input:    "undefined: ${undefined}",
			expected: "undefined: ${undefined}",
		},
		"partial variable name": {
			input:    "partial: ${region",
			expected: "partial: ${region",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			result := resolveVars(tc.input, vars)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestConfigVarResolution(t *testing.T) {
	// Create a test config file with variables
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "vars-sonic.yml")

	configData := []byte(`
version: "1"
project:
  name: "test-project"
  language: "go"
  root: "."
stages:
  deploy:
    runner: "kubernetes"
    commands:
      - "kubectl apply -f k8s/"
    volumes:
      - type: bind
        source: "${HOME}/.kube/${region.name}/config"
        target: "/root/.kube/config"
        readonly: true
    environment:
      KUBECONFIG: "/root/.kube/config"
      REGION: "${region.name}"
      ENV: "${env}"
`)

	err := os.WriteFile(configPath, configData, 0644)
	assert.NoError(t, err)

	// Test loading with variables
	vars := execVars{
		"HOME":        "/home/user",
		"region.name": "us-east-1",
		"env":         "prod",
	}

	config, err := loadConfig(configPath, vars)
	assert.NoError(t, err)

	// Verify variables were resolved during loading
	deploy := config.Stages["deploy"]
	assert.Equal(t, "/home/user/.kube/us-east-1/config", deploy.Volumes[0].Source)
	assert.Equal(t, "us-east-1", deploy.Environment["REGION"])
	assert.Equal(t, "prod", deploy.Environment["ENV"])

	// Test loading without variables
	config, err = loadConfig(configPath, nil)
	assert.NoError(t, err)

	// Verify unresolved variables are left as-is
	deploy = config.Stages["deploy"]
	assert.Equal(t, "${HOME}/.kube/${region.name}/config", deploy.Volumes[0].Source)
	assert.Equal(t, "${region.name}", deploy.Environment["REGION"])
	assert.Equal(t, "${env}", deploy.Environment["ENV"])
}

func TestLoadConfigWithS3Audit(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-s3-audit.yml")

	configData := []byte(`
version: "1"
project:
  name: "test-project"
  language: "go"
  root: "."
audit:
  store: "s3"
  path: "test-logs"
  s3bucket: "test-bucket"
  aws_profile: "test-profile"
stages:
  test:
    runner: "golang"
    commands:
      - "go test ./..."
`)

	err := os.WriteFile(configPath, configData, 0644)
	assert.NoError(t, err)

	// Load the config
	config, err := loadConfig(configPath, nil)
	assert.NoError(t, err)

	// Check audit settings
	assert.Equal(t, "s3", config.Audit.Store)
	assert.Equal(t, "test-logs", config.Audit.Path)
	assert.Equal(t, "test-bucket", config.Audit.S3Bucket)
	assert.Equal(t, "test-profile", config.Audit.AWSProfile)
}

func TestExpandRunTargetsDedupes(t *testing.T) {
	stages := map[string]Stage{
		"lint":      {},
		"unit-test": {},
		"build":     {},
	}
	suites := map[string][]string{
		"smoke": {"lint", "unit-test"},
		"full":  {"unit-test", "build"},
	}

	got, err := expandRunTargets([]string{"smoke", "full"}, suites, stages)
	assert.NoError(t, err)
	assert.Equal(t, []string{"lint", "unit-test", "build"}, got)

	got, err = expandRunTargets([]string{"lint", "smoke"}, suites, stages)
	assert.NoError(t, err)
	assert.Equal(t, []string{"lint", "unit-test"}, got)

	got, err = expandRunTargets([]string{"full", "smoke"}, suites, stages)
	assert.NoError(t, err)
	assert.Equal(t, []string{"unit-test", "build", "lint"}, got)

	_, err = expandRunTargets([]string{"missing"}, suites, stages)
	unknown, ok := err.(unknownTargetsError)
	assert.True(t, ok)
	assert.Equal(t, []string{"missing"}, unknown.names)
}

func TestSuiteValidation(t *testing.T) {
	tests := map[string]struct {
		config string
		want   string
	}{
		"unknown stage": {
			config: `
stages:
  lint:
    runner: alpine
    commands: ["echo lint"]
suites:
  smoke:
    - missing
`,
			want: `suite "smoke" references unknown stage "missing"`,
		},
		"empty suite": {
			config: `
stages:
  lint:
    runner: alpine
    commands: ["echo lint"]
suites:
  smoke: []
`,
			want: `suite "smoke" has no stages`,
		},
		"duplicate stage": {
			config: `
stages:
  lint:
    runner: alpine
    commands: ["echo lint"]
suites:
  smoke:
    - lint
    - lint
`,
			want: `suite "smoke" lists stage "lint" more than once`,
		},
		"name clash": {
			config: `
stages:
  lint:
    runner: alpine
    commands: ["echo lint"]
suites:
  lint:
    - lint
`,
			want: `suite "lint" has the same name as a stage`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sonic.yml")
			assert.NoError(t, os.WriteFile(path, []byte(tc.config), 0644))
			_, err := loadConfig(path, nil)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

func TestRunSuiteExpandsInOrder(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "sonic.yml")
	auditPath := filepath.Join(t.TempDir(), "logs")
	configData := []byte(`
version: "1"
project:
  name: suite-project
stages:
  lint:
    runner: golang
    commands: ["echo lint"]
  unit-test:
    runner: golang
    commands: ["echo unit"]
  build:
    runner: golang
    requires: ["unit-test"]
    commands: ["echo build"]
suites:
  smoke:
    - lint
    - unit-test
  full:
    - unit-test
    - build
`)
	assert.NoError(t, os.WriteFile(configPath, configData, 0644))

	originalExec := lib.ExecDocker
	defer func() { lib.ExecDocker = originalExec }()
	var mu sync.Mutex
	var ran []string
	lib.ExecDocker = func(args []string) lib.DockerResult {
		joined := strings.Join(args, " ")
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(joined, "echo lint"):
			ran = append(ran, "lint")
			return lib.DockerResult{Stdout: "lint ok\n"}
		case strings.Contains(joined, "echo unit"):
			ran = append(ran, "unit-test")
			return lib.DockerResult{Stdout: "unit ok\n"}
		case strings.Contains(joined, "echo build"):
			ran = append(ran, "build")
			return lib.DockerResult{Stdout: "build ok\n"}
		default:
			return lib.DockerResult{Error: fmt.Errorf("unexpected command %s", joined), ExitCode: 1}
		}
	}

	oldGoTest := os.Getenv("GO_TEST")
	os.Setenv("GO_TEST", "1")
	defer os.Setenv("GO_TEST", oldGoTest)

	runSuite := func(args ...string) {
		t.Helper()
		mu.Lock()
		ran = nil
		mu.Unlock()
		cmd := []string{"gosonic", "--sonic-file", configPath, "--audit-path", auditPath, "run"}
		_, stderr, err := captureOutput(func() error {
			return run(append(cmd, args...))
		})
		assert.NoError(t, err, stderr)
	}

	runSuite("smoke")
	mu.Lock()
	assert.Equal(t, []string{"lint", "unit-test"}, ran)
	mu.Unlock()

	runSuite("smoke", "build")
	mu.Lock()
	assert.Equal(t, []string{"lint", "unit-test", "build"}, ran)
	mu.Unlock()

	runSuite("full", "smoke")
	mu.Lock()
	assert.Equal(t, []string{"unit-test", "build", "lint"}, ran)
	mu.Unlock()

	_, stderr, err := captureOutput(func() error {
		return run([]string{"gosonic", "--sonic-file", configPath, "run", "missing"})
	})
	assert.Error(t, err)
	assert.Contains(t, stderr, "Error: invalid stage(s): missing")
	assert.Contains(t, stderr, "Available suites:")
	assert.Contains(t, stderr, "  smoke - lint, unit-test")
	assert.Contains(t, stderr, "  full - unit-test, build")
	assert.Contains(t, stderr, "Available stages:")
	assert.Contains(t, stderr, "  lint - golang")

	helpOut, _, helpErr := captureOutput(func() error {
		return run([]string{"gosonic", "--sonic-file", configPath, "run", "--help"})
	})
	assert.NoError(t, helpErr)
	assert.Contains(t, helpOut, "Suites:")
	assert.Contains(t, helpOut, "smoke  lint, unit-test")
	assert.Contains(t, helpOut, "full   unit-test, build")
	assert.Less(t, strings.Index(helpOut, "full"), strings.Index(helpOut, "smoke"))

	bareOut, bareErrOut, bareErr := captureOutput(func() error {
		return run([]string{"gosonic", "--sonic-file", configPath, "run"})
	})
	assert.NoError(t, bareErr)
	assert.Contains(t, bareOut, "NAME:")
	assert.Contains(t, bareOut, "gosonic run")
	assert.Contains(t, bareOut, "DESCRIPTION:")
	assert.Contains(t, bareOut, "Suites:")
	assert.Contains(t, bareOut, "smoke  lint, unit-test")
	assert.Contains(t, bareOut, "Stages, dependencies first:")
	assert.Contains(t, bareOut, "OPTIONS:")
	assert.NotContains(t, bareErrOut, "no stages specified")
}

func TestSuiteMatrixAndParallelRequires(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "sonic.yml")
	auditPath := filepath.Join(t.TempDir(), "logs")
	configData := []byte(`
version: "1"
project:
  name: suite-matrix
matrix:
  shard:
    - name: engine
      priority: 1
    - name: storage
      priority: 2
stages:
  test:
    runner: golang
    matrix: [shard]
    commands: ["echo test"]
  build:
    runner: golang
    requires: ["test"]
    commands: ["echo build"]
suites:
  ci:
    - test
    - build
`)
	assert.NoError(t, os.WriteFile(configPath, configData, 0644))

	originalExec := lib.ExecDocker
	defer func() { lib.ExecDocker = originalExec }()
	var mu sync.Mutex
	var ran []string
	lib.ExecDocker = func(args []string) lib.DockerResult {
		joined := strings.Join(args, " ")
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(joined, "echo build"):
			ran = append(ran, "build")
			return lib.DockerResult{Stdout: "build ok\n"}
		case strings.Contains(joined, "shard=engine"):
			ran = append(ran, "engine")
			return lib.DockerResult{Stdout: "engine ok\n"}
		case strings.Contains(joined, "shard=storage"):
			ran = append(ran, "storage")
			return lib.DockerResult{Stdout: "storage ok\n"}
		default:
			return lib.DockerResult{Error: fmt.Errorf("unexpected command %s", joined), ExitCode: 1}
		}
	}

	oldGoTest := os.Getenv("GO_TEST")
	os.Setenv("GO_TEST", "1")
	defer os.Setenv("GO_TEST", oldGoTest)

	_, stderr, err := captureOutput(func() error {
		return run([]string{
			"gosonic", "--sonic-file", configPath, "--audit-path", auditPath,
			"run", "--jobs", "2", "ci",
		})
	})
	assert.NoError(t, err, stderr)
	mu.Lock()
	defer mu.Unlock()
	assert.ElementsMatch(t, []string{"engine", "storage", "build"}, ran)
	assert.Equal(t, "build", ran[len(ran)-1])
}
