package main

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIndependentStagesStartBeforeDependent(t *testing.T) {
	stages := map[string]Stage{
		"lint":  {},
		"test":  {},
		"build": {Requires: []string{"lint", "test"}},
	}

	var mu sync.Mutex
	var starts []string
	bothStarted := make(chan struct{})
	var once sync.Once

	err := scheduleStages([]string{"lint", "test", "build"}, stages, 4, func(name string) error {
		mu.Lock()
		starts = append(starts, name)
		independent := 0
		for _, started := range starts {
			if started == "lint" || started == "test" {
				independent++
			}
		}
		mu.Unlock()

		if name == "lint" || name == "test" {
			if independent == 2 {
				once.Do(func() { close(bothStarted) })
			}
			<-bothStarted
		}
		return nil
	})

	assert.NoError(t, err)
	assert.Len(t, starts, 3)
	assert.Equal(t, "build", starts[2])
	assert.ElementsMatch(t, []string{"lint", "test"}, starts[:2])
}

func TestReadyQueueFollowsCommandLineOrder(t *testing.T) {
	var starts []string
	err := scheduleStages([]string{"lint", "test", "package"}, map[string]Stage{
		"lint":    {},
		"test":    {},
		"package": {},
	}, 1, func(name string) error {
		starts = append(starts, name)
		return nil
	})
	assert.NoError(t, err)
	assert.Equal(t, []string{"lint", "test", "package"}, starts)
}

func TestCycleStartsNothing(t *testing.T) {
	called := false
	err := scheduleStages([]string{"a", "b"}, map[string]Stage{
		"a": {Requires: []string{"b"}},
		"b": {Requires: []string{"a"}},
	}, 2, func(name string) error {
		called = true
		return nil
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "stage dependency cycle")
	assert.False(t, called)
}

func TestFailureSkipsUnstartedStages(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	var started []string

	err := scheduleStages([]string{"lint", "test", "package", "build"}, map[string]Stage{
		"lint":    {},
		"test":    {},
		"package": {},
		"build":   {Requires: []string{"test"}},
	}, 2, func(name string) error {
		mu.Lock()
		started = append(started, name)
		mu.Unlock()
		switch name {
		case "test":
			close(release)
			return fmt.Errorf("boom")
		case "lint":
			<-release
			return nil
		default:
			return nil
		}
	})

	assert.EqualError(t, err, `stage "test" failed: boom`)
	mu.Lock()
	defer mu.Unlock()
	assert.ElementsMatch(t, []string{"lint", "test"}, started)
}
