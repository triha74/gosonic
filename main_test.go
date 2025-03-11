package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gosonic/lib"

	"flag"

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

func TestStageOrder(t *testing.T) {
	// Create a temporary config file with specifically ordered stages
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "ordered-sonic.yml")

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

	config, err := loadConfig(configPath, nil)
	assert.NoError(t, err)

	// Define the expected order
	expectedOrder := []string{
		"unit-test",
		"build",
		"deploy",
	}

	// Verify the order matches exactly using StageOrder
	assert.Equal(t, expectedOrder, config.StageOrder, "Stage order should be preserved exactly as defined in YAML")

	// Also verify that all stages exist in the map
	for _, stageName := range expectedOrder {
		assert.Contains(t, config.Stages, stageName, "Stage %s should exist in the stages map", stageName)
	}
}

func TestStageExecution(t *testing.T) {
	// Set GO_TEST environment variable
	oldGoTest := os.Getenv("GO_TEST")
	os.Setenv("GO_TEST", "1")
	defer func() { os.Setenv("GO_TEST", oldGoTest) }()

	// Store original docker execution function
	originalDockerExec := lib.ExecDocker
	defer func() { lib.ExecDocker = originalDockerExec }()

	// Create a test config file
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
		args       []string
		wantStdout []string
		wantStderr []string
		wantErr    bool
	}{
		"run all stages in order": {
			args: []string{"gosonic", "--sonic-file", configPath, "run", "unit-test", "build", "deploy"},
			wantStdout: []string{
				"Tests passed",
				"Build successful",
				"Deployment complete",
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

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr string
			var err error

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
				for _, want := range tc.wantStderr {
					if !strings.Contains(stderr, want) {
						t.Errorf("Expected stderr to contain %q but got:\n%s", want, stderr)
					}
				}
			}
		})
	}
}

func TestCreateAuditStore(t *testing.T) {
	tmpDir := t.TempDir()

	// Mock S3 client creation
	originalCreateS3Client := createS3Client
	createS3Client = func(ctx context.Context) (lib.S3Client, error) {
		return &lib.MockS3Client{}, nil
	}
	defer func() { createS3Client = originalCreateS3Client }()

	tests := map[string]struct {
		config     *Config
		flags      []string
		env        map[string]string
		wantType   string
		wantPath   string
		wantBucket string
		wantErr    bool
	}{
		"default config": {
			config:   &Config{},
			wantType: "file",
			wantPath: ".logs",
		},
		"file store from config": {
			config: &Config{
				Audit: struct {
					Store    string `yaml:"store"`
					Path     string `yaml:"path"`
					S3Bucket string `yaml:"s3bucket"`
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
					Store    string `yaml:"store"`
					Path     string `yaml:"path"`
					S3Bucket string `yaml:"s3bucket"`
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
					Store    string `yaml:"store"`
					Path     string `yaml:"path"`
					S3Bucket string `yaml:"s3bucket"`
				}{
					Store:    "file",
					Path:     "config-logs",
					S3Bucket: "config-bucket",
				},
			},
			flags: []string{
				"--audit-store", "s3",
				"--audit-path", "cli-logs",
				"--audit-s3-bucket", "cli-bucket",
			},
			wantType:   "s3",
			wantPath:   "cli-logs",
			wantBucket: "cli-bucket",
		},
		"env vars override config": {
			config: &Config{
				Audit: struct {
					Store    string `yaml:"store"`
					Path     string `yaml:"path"`
					S3Bucket string `yaml:"s3bucket"`
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
					Store    string `yaml:"store"`
					Path     string `yaml:"path"`
					S3Bucket string `yaml:"s3bucket"`
				}{
					Store: "s3",
					Path:  "logs",
				},
			},
			wantErr: true,
		},
		"invalid store type": {
			config: &Config{
				Audit: struct {
					Store    string `yaml:"store"`
					Path     string `yaml:"path"`
					S3Bucket string `yaml:"s3bucket"`
				}{
					Store: "invalid",
				},
			},
			wantErr: true,
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
				err := set.Parse(tc.flags)
				assert.NoError(t, err)
			}

			ctx := cli.NewContext(app, set, nil)

			// Test store creation
			store, err := createAuditStore(tc.config, ctx)
			if tc.wantErr {
				assert.Error(t, err)
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

	// Verify usage information
	assert.Contains(t, stdout, "NAME:")
	assert.Contains(t, stdout, "gosonic - A build tool for CI/CD pipelines")
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
