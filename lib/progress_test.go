package lib

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFormatStatusLine(t *testing.T) {
	got := FormatStatusLine("lint", "ok", 4700*time.Millisecond, false)
	assert.Equal(t, "lint             ok      4.7s", got)
	got = FormatStatusLine("link-check", "failed", 1200*time.Millisecond, false)
	assert.Contains(t, got, "link-check")
	assert.Contains(t, got, "failed")
	assert.Contains(t, got, "1.2s")
	got = FormatStatusLine("lint", "skipped", 0, false)
	assert.Equal(t, "lint             skipped", got)
	got = FormatStatusLine("lint", "ok", time.Second, true)
	assert.Equal(t, "lint | ok 1.0s", got)
	got = FormatStatusLine("engine", "cancelled", time.Second, true)
	assert.Equal(t, "engine | cancelled 1.0s", got)
}

func TestFormatPrefixLineAlignsColumns(t *testing.T) {
	const width = 20
	lint := formatPrefixLine("lint", "ok", "24.3s", width)
	storage := formatPrefixLine("test [shard=storage]", "ok", "1.2s", width)
	running := formatPrefixLine("test [shard=engine]", "⠋", "", width)
	skipped := formatPrefixLine("feature-bundles", "skipped", "", width)

	assert.Equal(t, "lint                 | ok         24.3s", lint)
	assert.Equal(t, "test [shard=storage] | ok          1.2s", storage)
	assert.Equal(t, "test [shard=engine]  | ⠋", running)
	assert.Equal(t, "feature-bundles      | skipped", skipped)

	pipe := strings.Index(lint, " | ")
	for _, line := range []string{storage, running, skipped} {
		assert.Equal(t, pipe, strings.Index(line, " | "))
	}
	status := strings.Index(lint, "ok")
	assert.Equal(t, status, strings.Index(storage, "ok"))
	assert.Equal(t, status, strings.Index(running, "⠋"))
	assert.Equal(t, status, strings.Index(skipped, "skipped"))
	assert.Equal(t, len(lint), len(storage))
}

func TestProgressFinishNonTTYHasNoSpinner(t *testing.T) {
	originalTTY := stdoutIsTTY
	stdoutIsTTY = func() bool { return false }
	t.Cleanup(func() { stdoutIsTTY = originalTTY })

	stdout, _ := captureFD(t, func() {
		p := StartProgress("lint", false, false, nil)
		time.Sleep(20 * time.Millisecond)
		p.Finish("ok")
	})
	assert.NotContains(t, stdout, "\r")
	assert.NotContains(t, stdout, "/")
	assert.Contains(t, stdout, "lint")
	assert.Contains(t, stdout, "ok")
	assert.NotContains(t, stdout, "|")
	assert.False(t, containsSpinnerFrame(stdout))
}

func TestVerboseProgressIsSilent(t *testing.T) {
	stdout, _ := captureFD(t, func() {
		p := StartProgress("lint", true, false, nil)
		p.Finish("ok")
	})
	assert.Empty(t, stdout)
}

func TestWriteCapturedOutput(t *testing.T) {
	err := &StageOutputError{
		Err:    fmt.Errorf("exit status 1"),
		Stderr: "Root dir must be an absolute path: 'docs'\n",
	}
	stdout, stderr := captureFD(t, func() {
		WriteCapturedOutput("link-check", err, false, nil)
	})
	assert.Contains(t, stderr, "Root dir must be an absolute path")
	assert.True(t, strings.HasPrefix(stdout, "\n") || strings.Contains(stdout, "\n"))
}

func TestWriteCapturedOutputPrefixed(t *testing.T) {
	err := &StageOutputError{
		Err:    fmt.Errorf("exit status 1"),
		Stdout: "boom\n",
	}
	var mu sync.Mutex
	stdout, _ := captureFD(t, func() {
		WriteCapturedOutput("lint", err, true, &mu)
	})
	assert.Contains(t, stdout, "lint | boom")
}

func TestWriteCapturedOutputIgnoresBareError(t *testing.T) {
	stdout, stderr := captureFD(t, func() {
		WriteCapturedOutput("lint", fmt.Errorf("exit status 1"), false, nil)
	})
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
}

func TestParallelProgressDrawsASpinnerPerJobOnStart(t *testing.T) {
	originalTTY := stdoutIsTTY
	stdoutIsTTY = func() bool { return true }
	t.Cleanup(func() { stdoutIsTTY = originalTTY })

	stdout, _ := captureFD(t, func() {
		set := NewProgressSet(false, true)
		set.Start("lint")
		set.Start("test [shard=engine]")
		time.Sleep(30 * time.Millisecond)
		set.Close()
	})
	assert.Contains(t, stdout, "lint |")
	assert.Contains(t, stdout, "test [shard=engine] |")
	assert.True(t, containsSpinnerFrame(stdout))
}

func TestParallelProgressFinishKeepsOtherJobs(t *testing.T) {
	originalTTY := stdoutIsTTY
	stdoutIsTTY = func() bool { return true }
	t.Cleanup(func() { stdoutIsTTY = originalTTY })

	stdout, _ := captureFD(t, func() {
		set := NewProgressSet(false, true)
		lint := set.Start("lint")
		test := set.Start("test")
		set.Finish(lint, "ok", nil)
		set.Finish(test, "ok", nil)
		set.Close()
	})
	assert.Contains(t, stdout, "lint | ok")
	assert.Contains(t, stdout, "test | ok")
}

func TestProgressSummarizeReportsStatusAndWallTime(t *testing.T) {
	originalTTY := stdoutIsTTY
	stdoutIsTTY = func() bool { return false }
	t.Cleanup(func() { stdoutIsTTY = originalTTY })

	stdout, _ := captureFD(t, func() {
		set := NewProgressSet(false, false, "lint")
		lint := set.Start("lint")
		set.Finish(lint, "ok", nil)
		set.Close()
		set.Summarize(nil)
	})
	assert.Regexp(t, `(?m)^run\s+ok\s+\d+\.\d+s$`, stdout)

	stdout, _ = captureFD(t, func() {
		set := NewProgressSet(false, true, "lint", "unit-test")
		lint := set.Start("lint")
		set.Finish(lint, "failed", fmt.Errorf("exit status 1"))
		set.Close()
		set.Summarize(fmt.Errorf("stage %q failed", "lint"))
	})
	assert.Regexp(t, `(?m)^run\s+\| failed\s+\d+\.\d+s$`, stdout)
	var pipes []int
	for _, line := range strings.Split(stdout, "\n") {
		if col := strings.Index(line, " | "); col >= 0 {
			pipes = append(pipes, col)
		}
	}
	assert.GreaterOrEqual(t, len(pipes), 2)
	assert.Equal(t, pipes[0], pipes[len(pipes)-1])

	stdout, _ = captureFD(t, func() {
		set := NewProgressSet(true, false, "lint")
		set.Close()
		set.Summarize(context.Canceled)
	})
	assert.Regexp(t, `(?m)^run\s+cancelled\s+\d+\.\d+s$`, stdout)
}

func containsSpinnerFrame(s string) bool {
	for _, frame := range spinnerFrames {
		if strings.ContainsRune(s, frame) {
			return true
		}
	}
	return false
}
