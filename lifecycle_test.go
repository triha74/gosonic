package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gosonic/lib"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
)

func TestMatrixExpansionOrder(t *testing.T) {
	config := &Config{
		Matrix: MatrixAxes{
			"shard": {
				{Name: "planner", Priority: 2},
				{Name: "engine", Priority: 1},
			},
		},
		Stages: map[string]Stage{
			"test": {Matrix: []string{"shard"}, Runner: "rust"},
		},
	}
	names, stages, err := expandMatrixStages([]string{"test"}, config, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"test [shard=engine]", "test [shard=planner]"}, names)
	assert.Equal(t, "engine", stages[names[0]].Environment["shard"])

	names, _, err = expandMatrixStages([]string{"test"}, config, map[string][]string{"shard": {"engine"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"test [shard=engine]"}, names)
}

func TestInlineActionExpansion(t *testing.T) {
	config := &Config{Actions: map[string]Action{
		"setup": {Commands: []string{"rustup show"}},
		"build": {Runner: "docker:cli", Commands: []string{"docker build ."}},
	}}
	segments, err := partitionBody(Stage{
		Commands: []string{"cargo test"},
		Steps: []Step{
			{Uses: "setup"},
			{Run: "cargo test"},
			{Uses: "build"},
		},
	}, config)
	require.NoError(t, err)
	require.Len(t, segments, 2)
	assert.Equal(t, []string{"rustup show", "cargo test"}, segments[0].commands)
	assert.Equal(t, "build", segments[1].uses)
}

func TestOnceActionRunsOnce(t *testing.T) {
	original := lib.ExecDocker
	defer func() { lib.ExecDocker = original }()
	var mu sync.Mutex
	var builds int
	lib.ExecDocker = func(args []string) lib.DockerResult {
		if strings.Contains(strings.Join(args, " "), "docker build") {
			mu.Lock()
			builds++
			mu.Unlock()
		}
		return lib.DockerResult{}
	}

	config := &Config{
		Project: struct {
			Name     string `yaml:"name"`
			Language string `yaml:"language"`
			Root     string `yaml:"root"`
		}{Name: "once"},
		Audit: struct {
			Store      string `yaml:"store"`
			Path       string `yaml:"path"`
			S3Bucket   string `yaml:"s3bucket"`
			AWSProfile string `yaml:"aws_profile"`
		}{Store: "file", Path: t.TempDir()},
		Actions: map[string]Action{
			"build-image": {
				Runner:       "docker:cli",
				Commands:     []string{"docker build ."},
				Once:         true,
				DockerSocket: true,
			},
		},
	}
	stage := func(name string) Stage {
		return Stage{
			Runner:   "alpine",
			Commands: []string{"echo " + name},
			Before:   []HookRef{{Uses: "build-image"}},
		}
	}
	gate := &actionOnce{}
	var wg sync.WaitGroup
	wg.Add(2)
	for _, name := range []string{"lint", "test"} {
		name := name
		go func() {
			defer wg.Done()
			err := withCLI(func(ctx *cli.Context) error {
				return runConfiguredStage(ctx, name, stage(name), config, nil, false, nil, nil, gate)
			})
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, builds)
}

func TestAfterRunsWhenStageFails(t *testing.T) {
	original := lib.ExecDocker
	defer func() { lib.ExecDocker = original }()
	var mu sync.Mutex
	var ran []string
	lib.ExecDocker = func(args []string) lib.DockerResult {
		joined := strings.Join(args, " ")
		mu.Lock()
		ran = append(ran, joined)
		mu.Unlock()
		if strings.Contains(joined, "cargo test") {
			return lib.DockerResult{Error: assert.AnError, ExitCode: 1}
		}
		return lib.DockerResult{}
	}
	config := &Config{
		Project: struct {
			Name     string `yaml:"name"`
			Language string `yaml:"language"`
			Root     string `yaml:"root"`
		}{Name: "after"},
		Audit: struct {
			Store      string `yaml:"store"`
			Path       string `yaml:"path"`
			S3Bucket   string `yaml:"s3bucket"`
			AWSProfile string `yaml:"aws_profile"`
		}{Store: "file", Path: t.TempDir()},
		Actions: map[string]Action{
			"cleanup": {Runner: "alpine", Commands: []string{"echo cleanup"}},
		},
	}
	err := withCLI(func(ctx *cli.Context) error {
		return runConfiguredStage(ctx, "test", Stage{
			Runner:   "rust",
			Commands: []string{"cargo test"},
			After:    []HookRef{{Uses: "cleanup"}},
		}, config, nil, false, nil, nil, &actionOnce{})
	})
	assert.Error(t, err)
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(ran, "\n")
	assert.Contains(t, joined, "cargo test")
	assert.Contains(t, joined, "echo cleanup")
}

func TestWhenSkipsStage(t *testing.T) {
	original := lib.ExecDocker
	defer func() { lib.ExecDocker = original }()
	called := false
	lib.ExecDocker = func(args []string) lib.DockerResult {
		called = true
		return lib.DockerResult{}
	}
	config := &Config{
		Project: struct {
			Name     string `yaml:"name"`
			Language string `yaml:"language"`
			Root     string `yaml:"root"`
		}{Name: "skip"},
		Audit: struct {
			Store      string `yaml:"store"`
			Path       string `yaml:"path"`
			S3Bucket   string `yaml:"s3bucket"`
			AWSProfile string `yaml:"aws_profile"`
		}{Store: "file", Path: t.TempDir()},
	}
	err := withCLI(func(ctx *cli.Context) error {
		return runConfiguredStage(ctx, "aura", Stage{
			Runner:   "rust",
			Commands: []string{"cargo test"},
			When:     "env:NEO4J_URI",
		}, config, nil, false, nil, nil, &actionOnce{})
	})
	assert.NoError(t, err)
	assert.False(t, called)
}

func TestArtifactsAndCoverage(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "report.txt"), []byte("ok"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cover.out"), []byte("70\n"), 0644))
	wd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(wd) })

	err = collectArtifacts("test", Stage{Artifacts: []string{"report.txt"}})
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(".sonic-artifacts", "test", "report.txt"))
	assert.NoError(t, err)

	err = checkCoverage(Stage{Coverage: &Coverage{Enabled: true, Threshold: 80, File: "cover.out", Format: "percent"}})
	assert.Error(t, err)
	err = checkCoverage(Stage{Coverage: &Coverage{Enabled: true, Threshold: 60, File: "cover.out", Format: "percent"}})
	assert.NoError(t, err)
}

func withCLI(fn func(ctx *cli.Context) error) error {
	app := &cli.App{
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "audit-store"},
			&cli.StringFlag{Name: "audit-path"},
			&cli.StringFlag{Name: "audit-s3-bucket"},
			&cli.BoolFlag{Name: "verbose"},
		},
		Action: fn,
	}
	return app.Run([]string{"gosonic"})
}
