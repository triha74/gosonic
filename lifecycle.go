package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gosonic/lib"

	"github.com/urfave/cli/v2"
)

type actionOnce struct {
	mu    sync.Mutex
	calls map[string]*onceCall
}

type onceCall struct {
	done bool
	err  error
	wait chan struct{}
}

func (g *actionOnce) Do(name string, fn func() error) error {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = map[string]*onceCall{}
	}
	if call, ok := g.calls[name]; ok {
		g.mu.Unlock()
		<-call.wait
		return call.err
	}
	call := &onceCall{wait: make(chan struct{})}
	g.calls[name] = call
	g.mu.Unlock()

	call.err = fn()
	close(call.wait)
	return call.err
}

type bodySegment struct {
	commands []string
	uses     string
	action   *Action
}

func validateActionRefs(config *Config) error {
	if config == nil {
		return nil
	}
	check := func(stageName, uses string) error {
		if uses == "" {
			return fmt.Errorf("stage %q has an empty action reference", stageName)
		}
		if _, ok := config.Actions[uses]; !ok {
			return fmt.Errorf("stage %q references unknown action %q", stageName, uses)
		}
		return nil
	}
	for name, stage := range config.Stages {
		refs := append([]HookRef{}, stage.Before...)
		refs = append(refs, stage.After...)
		for _, hook := range refs {
			if err := check(name, hook.Uses); err != nil {
				return err
			}
		}
		for _, step := range stage.Steps {
			if step.Uses == "" {
				continue
			}
			if err := check(name, step.Uses); err != nil {
				return err
			}
		}
	}
	return nil
}

func parseMatrixOverrides(values []string) (map[string][]string, error) {
	overrides := map[string][]string{}
	for _, value := range values {
		key, rest, ok := strings.Cut(value, "=")
		if !ok || key == "" || rest == "" {
			return nil, fmt.Errorf("invalid --matrix value %q", value)
		}
		var parts []string
		for _, part := range strings.Split(rest, ",") {
			if part != "" {
				parts = append(parts, part)
			}
		}
		if len(parts) == 0 {
			return nil, fmt.Errorf("invalid --matrix value %q", value)
		}
		overrides[key] = parts
	}
	return overrides, nil
}

func expandMatrixStages(names []string, config *Config, overrides map[string][]string) ([]string, map[string]Stage, error) {
	expanded := make(map[string]Stage)
	var order []string
	for _, name := range names {
		stage, ok := config.Stages[name]
		if !ok {
			return nil, nil, fmt.Errorf("unknown stage %q", name)
		}
		cells, err := matrixCells(name, stage, config.Matrix, overrides)
		if err != nil {
			return nil, nil, err
		}
		for _, cell := range cells {
			if _, exists := expanded[cell.name]; exists {
				return nil, nil, fmt.Errorf("duplicate stage instance %q", cell.name)
			}
			expanded[cell.name] = cell.stage
			order = append(order, cell.name)
		}
	}
	for name, stage := range expanded {
		expanded[name] = retargetRequires(stage, expanded)
	}
	return order, expanded, nil
}

type matrixCell struct {
	name  string
	stage Stage
}

func matrixCells(name string, stage Stage, axes MatrixAxes, overrides map[string][]string) ([]matrixCell, error) {
	if len(stage.Matrix) == 0 {
		return []matrixCell{{name: name, stage: cloneStage(stage)}}, nil
	}
	type axis struct {
		key    string
		values []string
	}
	var dims []axis
	for _, dim := range stage.Matrix {
		values, err := axisValues(dim, axes, overrides)
		if err != nil {
			return nil, fmt.Errorf("stage %q: %w", name, err)
		}
		dims = append(dims, axis{key: dim, values: values})
	}
	combos := [][][2]string{{}}
	for _, dim := range dims {
		var next [][][2]string
		for _, combo := range combos {
			for _, value := range dim.values {
				copied := append([][2]string{}, combo...)
				copied = append(copied, [2]string{dim.key, value})
				next = append(next, copied)
			}
		}
		combos = next
	}
	cells := make([]matrixCell, 0, len(combos))
	for _, combo := range combos {
		inst := cloneStage(stage)
		if inst.Environment == nil {
			inst.Environment = map[string]string{}
		}
		labels := make([]string, 0, len(combo))
		for _, pair := range combo {
			inst.Environment[pair[0]] = pair[1]
			labels = append(labels, pair[0]+"="+pair[1])
		}
		cells = append(cells, matrixCell{
			name:  name + " [" + strings.Join(labels, ",") + "]",
			stage: inst,
		})
	}
	return cells, nil
}

func axisValues(dim string, axes MatrixAxes, overrides map[string][]string) ([]string, error) {
	if values, ok := overrides[dim]; ok {
		return values, nil
	}
	defined, ok := axes[dim]
	if !ok || len(defined) == 0 {
		return nil, fmt.Errorf("matrix dimension %q is not defined", dim)
	}
	ordered := append([]MatrixValue{}, defined...)
	sort.SliceStable(ordered, func(a, b int) bool {
		return ordered[a].Priority < ordered[b].Priority
	})
	values := make([]string, len(ordered))
	for i, value := range ordered {
		values[i] = value.Name
	}
	return values, nil
}

func cloneStage(stage Stage) Stage {
	if stage.Environment != nil {
		env := make(map[string]string, len(stage.Environment))
		for k, v := range stage.Environment {
			env[k] = v
		}
		stage.Environment = env
	}
	stage.Requires = append([]string{}, stage.Requires...)
	stage.Commands = append([]string{}, stage.Commands...)
	return stage
}

func retargetRequires(stage Stage, expanded map[string]Stage) Stage {
	if len(stage.Requires) == 0 {
		return stage
	}
	var reqs []string
	for _, req := range stage.Requires {
		if _, ok := expanded[req]; ok {
			reqs = append(reqs, req)
			continue
		}
		prefix := req + " ["
		var cells []string
		for name := range expanded {
			if strings.HasPrefix(name, prefix) {
				cells = append(cells, name)
			}
		}
		sort.Strings(cells)
		if len(cells) > 0 {
			reqs = append(reqs, cells...)
			continue
		}
		reqs = append(reqs, req)
	}
	stage.Requires = reqs
	return stage
}

func partitionBody(stage Stage, config *Config) ([]bodySegment, error) {
	if len(stage.Steps) == 0 {
		return []bodySegment{{commands: append([]string{}, stage.Commands...)}}, nil
	}
	var segments []bodySegment
	var inline []string
	flush := func() {
		if len(inline) == 0 {
			return
		}
		segments = append(segments, bodySegment{commands: inline})
		inline = nil
	}
	for _, step := range stage.Steps {
		if step.Run != "" && step.Uses != "" {
			return nil, fmt.Errorf("a step cannot set both run and uses")
		}
		if step.Run == "" && step.Uses == "" {
			return nil, fmt.Errorf("a step must set run or uses")
		}
		if step.When != "" {
			ok, err := lib.EvalWhen(step.When, "")
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		if step.Run != "" {
			inline = append(inline, step.Run)
			continue
		}
		action, ok := config.Actions[step.Uses]
		if !ok {
			return nil, fmt.Errorf("unknown action %q", step.Uses)
		}
		if action.Runner == "" {
			inline = append(inline, action.Commands...)
			continue
		}
		flush()
		copied := action
		segments = append(segments, bodySegment{uses: step.Uses, action: &copied})
	}
	flush()
	return segments, nil
}

func prependInline(segments []bodySegment, commands []string) []bodySegment {
	if len(commands) == 0 {
		return segments
	}
	for i, segment := range segments {
		if segment.action == nil {
			segments[i].commands = append(append([]string{}, commands...), segment.commands...)
			return segments
		}
	}
	return append([]bodySegment{{commands: commands}}, segments...)
}

func runConfiguredStage(ctx *cli.Context, name string, stage Stage, config *Config, skipRequires map[string]bool, prefixOutput bool, outputMu *sync.Mutex, auditStore lib.AuditStore, gate *actionOnce) error {
	if gate == nil {
		gate = &actionOnce{}
	}
	ok, err := lib.EvalWhen(stage.When, "")
	if err != nil {
		return err
	}
	if !ok {
		return recordStatus(ctx, config, name, "skipped", auditStore)
	}
	if auditStore == nil {
		auditStore, err = createAuditStore(config, ctx)
		if err != nil {
			return fmt.Errorf("creating audit store: %w", err)
		}
	}
	gitRev, err := getGitRevision()
	if err != nil {
		gitRev = "unknown"
	}
	if err := verifyRequirements(withoutRequires(stage, skipRequires), auditStore, config.Project.Name, gitRev); err != nil {
		return fmt.Errorf("stage requirements not met: %w", err)
	}

	var inlineBefore []string
	for _, hook := range stage.Before {
		commands, err := runHook(ctx, hook, stage, config, prefixOutput, outputMu, auditStore, gate, "")
		if err != nil {
			return err
		}
		inlineBefore = append(inlineBefore, commands...)
	}

	segments, err := partitionBody(stage, config)
	if err != nil {
		return err
	}
	segments = prependInline(segments, inlineBefore)

	var bodyErr error
	for i, segment := range segments {
		if segment.action != nil {
			bodyErr = runAction(ctx, segment.uses, *segment.action, stage, config, prefixOutput, outputMu, auditStore, gate)
		} else if len(segment.commands) > 0 || stage.Runner != "" {
			bodyStage := stage
			bodyStage.Commands = segment.commands
			bodyStage.Steps = nil
			auditName := name
			if len(segments) > 1 {
				auditName = fmt.Sprintf("%s#%d", name, i+1)
			}
			bodyErr = executeStage(ctx, auditName, bodyStage, config, skipRequires, prefixOutput, outputMu, auditStore)
		}
		if bodyErr != nil {
			break
		}
	}
	if bodyErr == nil {
		if err := collectArtifacts(name, stage); err != nil {
			bodyErr = err
		} else if err := checkCoverage(stage); err != nil {
			bodyErr = err
		}
	}
	status := "success"
	if bodyErr != nil {
		status = "error"
	}
	if err := recordStatus(ctx, config, name, status, auditStore); err != nil && bodyErr == nil {
		bodyErr = err
	}

	outcome := "success"
	if bodyErr != nil {
		outcome = "failure"
	}
	var afterErr error
	for _, hook := range stage.After {
		when := hook.When
		if when == "" {
			when = "always"
		}
		_, err := runHook(ctx, HookRef{Uses: hook.Uses, When: when}, stage, config, prefixOutput, outputMu, auditStore, gate, outcome)
		if err != nil {
			afterErr = errors.Join(afterErr, err)
		}
	}
	return errors.Join(bodyErr, afterErr)
}

func runHook(ctx *cli.Context, hook HookRef, stage Stage, config *Config, prefixOutput bool, outputMu *sync.Mutex, auditStore lib.AuditStore, gate *actionOnce, outcome string) ([]string, error) {
	when := hook.When
	if outcome != "" && when == "" {
		when = "always"
	}
	ok, err := lib.EvalWhen(when, outcome)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	action, ok := config.Actions[hook.Uses]
	if !ok {
		return nil, fmt.Errorf("unknown action %q", hook.Uses)
	}
	if action.Runner == "" && !action.Once && outcome == "" {
		return append([]string{}, action.Commands...), nil
	}
	if err := runAction(ctx, hook.Uses, action, stage, config, prefixOutput, outputMu, auditStore, gate); err != nil {
		return nil, err
	}
	return nil, nil
}

func runAction(ctx *cli.Context, uses string, action Action, stage Stage, config *Config, prefixOutput bool, outputMu *sync.Mutex, auditStore lib.AuditStore, gate *actionOnce) error {
	run := func() error {
		actionStage := Stage{
			Runner:           action.Runner,
			Commands:         action.Commands,
			Environment:      action.Environment,
			Volumes:          action.Volumes,
			Timeout:          action.Timeout,
			WorkingDirectory: action.WorkingDirectory,
			DockerSocket:     action.DockerSocket,
		}
		if actionStage.Runner == "" {
			actionStage.Runner = stage.Runner
			actionStage.WorkingDirectory = firstNonEmpty(action.WorkingDirectory, stage.WorkingDirectory)
			actionStage.DockerSocket = action.DockerSocket || stage.DockerSocket
		}
		return executeStage(ctx, "action:"+uses, actionStage, config, nil, prefixOutput, outputMu, auditStore)
	}
	if action.Once {
		return gate.Do(uses, run)
	}
	return run()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func recordStatus(ctx *cli.Context, config *Config, name, status string, auditStore lib.AuditStore) error {
	var err error
	if auditStore == nil {
		auditStore, err = createAuditStore(config, ctx)
		if err != nil {
			return err
		}
	}
	gitRev, err := getGitRevision()
	if err != nil {
		gitRev = "unknown"
	}
	return auditStore.Store(lib.AuditLog{
		Project:     config.Project.Name,
		GitRevision: gitRev,
		Stage:       name,
		Status:      status,
		StartTime:   time.Now(),
	})
}

func hostWorkspace(stage Stage) string {
	workdir := stage.WorkingDirectory
	if workdir == "" || workdir == "/workspace" {
		return "."
	}
	rel := strings.TrimPrefix(workdir, "/workspace")
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return "."
	}
	return rel
}

func collectArtifacts(stageName string, stage Stage) error {
	if len(stage.Artifacts) == 0 {
		return nil
	}
	base := hostWorkspace(stage)
	destRoot := filepath.Join(".sonic-artifacts", sanitizePath(stageName))
	for _, rel := range stage.Artifacts {
		src := filepath.Join(base, rel)
		info, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("artifact %q: %w", rel, err)
		}
		dest := filepath.Join(destRoot, rel)
		if info.IsDir() {
			if err := copyDir(src, dest); err != nil {
				return fmt.Errorf("artifact %q: %w", rel, err)
			}
			continue
		}
		if err := copyFile(src, dest); err != nil {
			return fmt.Errorf("artifact %q: %w", rel, err)
		}
	}
	return nil
}

func checkCoverage(stage Stage) error {
	if stage.Coverage == nil || !stage.Coverage.Enabled {
		return nil
	}
	if stage.Coverage.File == "" {
		return fmt.Errorf("coverage is enabled but no file is set")
	}
	data, err := os.ReadFile(filepath.Join(hostWorkspace(stage), stage.Coverage.File))
	if err != nil {
		return fmt.Errorf("reading coverage file: %w", err)
	}
	percent, err := lib.CoveragePercent(string(data), stage.Coverage.Format)
	if err != nil {
		return err
	}
	if percent < float64(stage.Coverage.Threshold) {
		return fmt.Errorf("coverage %.1f%% is below threshold %d%%", percent, stage.Coverage.Threshold)
	}
	return nil
}

func sanitizePath(value string) string {
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func copyFile(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func copyDir(src, dest string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		return copyFile(path, target)
	})
}
