package main

import (
	"fmt"
	"strings"
	"sync"
)

// scheduleStages runs independent stages concurrently, up to jobs at a time.
// requires edges whose targets are also in names form the dependency graph.
// Requirements outside this invocation are ignored here; the caller checks
// those against the audit store before starting. jobs must be at least 1.
// The default command path keeps jobs == 1 on the sequential loop instead.
func scheduleStages(names []string, stages map[string]Stage, jobs int, run func(name string) error) error {
	if jobs < 1 {
		return fmt.Errorf("jobs must be at least 1")
	}
	if len(names) == 0 {
		return fmt.Errorf("no stages specified")
	}

	inRun := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, dup := inRun[name]; dup {
			return fmt.Errorf("duplicate stage %q", name)
		}
		if _, ok := stages[name]; !ok {
			return fmt.Errorf("unknown stage %q", name)
		}
		inRun[name] = struct{}{}
	}

	// dependents maps a stage to the stages that list it in requires.
	// Appending in command-line order keeps that order when several become ready together.
	dependents := make(map[string][]string, len(names))
	indegree := make(map[string]int, len(names))
	for _, name := range names {
		for _, req := range stages[name].Requires {
			if _, ok := inRun[req]; !ok {
				continue
			}
			dependents[req] = append(dependents[req], name)
			indegree[name]++
		}
	}

	if cycle := findCycle(names, dependents); len(cycle) > 0 {
		return fmt.Errorf("stage dependency cycle: %s", strings.Join(cycle, " -> "))
	}

	ready := make([]string, 0, len(names))
	for _, name := range names {
		if indegree[name] == 0 {
			ready = append(ready, name)
		}
	}

	var mu sync.Mutex
	cond := sync.NewCond(&mu)
	var firstErr error
	cancelled := false
	running := 0

	worker := func() {
		for {
			mu.Lock()
			for !cancelled && len(ready) == 0 && running > 0 {
				cond.Wait()
			}
			if cancelled || len(ready) == 0 {
				mu.Unlock()
				return
			}
			name := ready[0]
			ready = ready[1:]
			running++
			mu.Unlock()

			err := run(name)

			mu.Lock()
			running--
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("stage %q failed: %w", name, err)
				}
				cancelled = true
				ready = nil
				cond.Broadcast()
				mu.Unlock()
				return
			}
			if cancelled {
				cond.Broadcast()
				mu.Unlock()
				return
			}
			for _, dep := range dependents[name] {
				indegree[dep]--
				if indegree[dep] == 0 {
					ready = append(ready, dep)
				}
			}
			cond.Broadcast()
			mu.Unlock()
		}
	}

	workerCount := jobs
	if workerCount > len(names) {
		workerCount = len(names)
	}
	var wg sync.WaitGroup
	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer wg.Done()
			worker()
		}()
	}
	wg.Wait()
	return firstErr
}

// findCycle reports a dependency cycle as a closed path (first stage repeated at the end).
// dependents edges point from a requirement to the stages that wait on it.
func findCycle(names []string, dependents map[string][]string) []string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(names))
	var stack []string
	var cycle []string

	var visit func(string) bool
	visit = func(name string) bool {
		color[name] = gray
		stack = append(stack, name)
		for _, next := range dependents[name] {
			switch color[next] {
			case gray:
				for _, stacked := range stack {
					if len(cycle) > 0 || stacked == next {
						cycle = append(cycle, stacked)
					}
				}
				cycle = append(cycle, next)
				return true
			case white:
				if visit(next) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[name] = black
		return false
	}

	for _, name := range names {
		if color[name] == white && visit(name) {
			return cycle
		}
	}
	return nil
}
