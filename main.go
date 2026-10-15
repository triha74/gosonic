package main

import (
	"bytes"
	"context"
	"fmt"
	"gosonic/lib"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

// For testing purposes
var (
	createS3Client = func(verbose bool) (lib.S3Client, error) {
		// Create context for AWS operations
		ctx := context.Background()
		// Look for audit-log specific credentials first
		auditLogProfile := os.Getenv("AUDIT_LOG_AWS_PROFILE")
		auditLogAccessKey := os.Getenv("AUDIT_LOG_AWS_ACCESS_KEY_ID")
		auditLogSecretKey := os.Getenv("AUDIT_LOG_AWS_SECRET_ACCESS_KEY")
		auditLogSessionToken := os.Getenv("AUDIT_LOG_AWS_SESSION_TOKEN")
		auditLogRegion := os.Getenv("AUDIT_LOG_AWS_REGION")
		// Configure AWS SDK options based on profile and region

		var opts []func(*awsconfig.LoadOptions) error

		// Add profile if specified
		if auditLogProfile != "" {
			opts = append(opts, awsconfig.WithSharedConfigProfile(auditLogProfile))
		} else if auditLogAccessKey != "" && auditLogSecretKey != "" && auditLogSessionToken != "" {
			opts = append(opts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(auditLogAccessKey, auditLogSecretKey, auditLogSessionToken)))
		}

		// Add region if specified
		if auditLogRegion != "" {
			opts = append(opts, awsconfig.WithRegion(auditLogRegion))
		}

		// Load AWS configuration with options
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)

		return s3.NewFromConfig(awsCfg), err
	}
)

const (
	defaultConfigFile = ".sonic/sonic.yml"
	legacyConfigFile  = ".sonic.yml"
	defaultRegistry   = "public.ecr.aws"
	defaultRunner     = "public.ecr.aws/docker/library/alpine:latest"
	defaultAuditStore = "file"
)

// resolveConfigPath returns an explicit --sonic-file path, or the first
// default that exists. Help still advertises defaultConfigFile.
func resolveConfigPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if _, err := os.Stat(defaultConfigFile); err == nil {
		return defaultConfigFile
	}
	if _, err := os.Stat(legacyConfigFile); err == nil {
		return legacyConfigFile
	}
	return defaultConfigFile
}

// defaultCreateAuditStore creates the appropriate audit store based on configuration
func defaultCreateAuditStore(config *Config, flags *cli.Context) (lib.AuditStore, error) {
	// Get verbose flag
	verbose := flags.Bool("verbose")

	// CLI flags take precedence over config file
	storeType := flags.String("audit-store")

	if storeType == "" {
		storeType = config.Audit.Store
	}
	if storeType == "" {
		storeType = defaultAuditStore
	}
	if verbose {
		fmt.Printf("Using audit store: %s\n", storeType)
	}
	switch storeType {
	case "file":
		path := flags.String("audit-path")
		if path == "" {
			path = config.Audit.Path
		}
		if path == "" {
			path = ".logs"
		}

		if verbose {
			fmt.Printf("Using file audit store at path: %s\n", path)
		}

		return lib.NewFileStore(path, verbose), nil

	case "s3":
		bucket := flags.String("audit-s3-bucket")
		if bucket == "" {
			bucket = config.Audit.S3Bucket
		}
		if bucket == "" {
			return nil, fmt.Errorf("s3 bucket must be specified for s3 audit store")
		}

		prefix := flags.String("audit-path")
		if prefix == "" {
			prefix = config.Audit.Path
		}

		// Get AWS profile from config if not set in environment
		awsProfile := os.Getenv("AUDIT_LOG_AWS_PROFILE")
		if awsProfile == "" && config.Audit.AWSProfile != "" {
			os.Setenv("AUDIT_LOG_AWS_PROFILE", config.Audit.AWSProfile)
			if verbose {
				fmt.Printf("Setting AUDIT_LOG_AWS_PROFILE from config: %s\n", config.Audit.AWSProfile)
			}
		}

		// Get S3 client
		client, err := createS3Client(verbose)
		if err != nil {
			return nil, fmt.Errorf("creating S3 client: %w", err)
		}

		return lib.NewS3Store(client, bucket, prefix, verbose), nil

	default:
		return nil, fmt.Errorf("unknown audit store type: %s", storeType)
	}
}

// Variables that can be overridden in tests
var (
	createAuditStore = defaultCreateAuditStore
	getGitRevision   = lib.GetGitRevision
	execDocker       = lib.ExecDocker
)

type Config struct {
	Version string `yaml:"version"`
	Project struct {
		Name     string `yaml:"name"`
		Language string `yaml:"language"`
		Root     string `yaml:"root"`
	} `yaml:"project"`
	Audit struct {
		Store      string `yaml:"store"`       // "file" or "s3"
		Path       string `yaml:"path"`        // Directory for file store or S3 prefix
		S3Bucket   string `yaml:"s3bucket"`    // S3 bucket name if using S3
		AWSProfile string `yaml:"aws_profile"` // AWS profile to use for S3
	} `yaml:"audit"`
	Runners Runners             `yaml:"runners,omitempty"`
	Matrix  MatrixAxes          `yaml:"matrix,omitempty"`
	Actions map[string]Action   `yaml:"actions,omitempty"`
	Suites  map[string][]string `yaml:"suites,omitempty"`
	Stages  map[string]Stage    `yaml:"stages"`
}

// Runners maps an alias to a Docker image reference.
// A mapping and a list of single-alias mappings are both accepted:
//
//	runners:
//	  alpine: "docker/library/golang:1.20-alpine3.17"
//
//	runners:
//	  - alpine: "docker/library/golang:1.20-alpine3.17"
type Runners map[string]string

func (r *Runners) UnmarshalYAML(value *yaml.Node) error {
	aliases := make(map[string]string)
	switch value.Kind {
	case yaml.MappingNode:
		if err := value.Decode(&aliases); err != nil {
			return err
		}
	case yaml.SequenceNode:
		for i, item := range value.Content {
			if item.Kind != yaml.MappingNode {
				return fmt.Errorf("runners[%d] must be a map of alias to image", i)
			}
			var entry map[string]string
			if err := item.Decode(&entry); err != nil {
				return fmt.Errorf("runners[%d]: %w", i, err)
			}
			if len(entry) != 1 {
				return fmt.Errorf("runners[%d] must contain one alias", i)
			}
			for alias, image := range entry {
				if _, exists := aliases[alias]; exists {
					return fmt.Errorf("duplicate runner alias %q", alias)
				}
				aliases[alias] = image
			}
		}
	default:
		return fmt.Errorf("runners must be a map or a list of alias maps")
	}

	for alias, image := range aliases {
		if alias == "" {
			return fmt.Errorf("runner alias is empty")
		}
		if image == "" {
			return fmt.Errorf("runner %q has an empty image", alias)
		}
	}
	*r = aliases
	return nil
}

type MatrixValue struct {
	Name     string `yaml:"name"`
	Priority int    `yaml:"priority"` // Lower numbers run first
}

// MatrixAxes maps a dimension name to its values, in priority order.
type MatrixAxes map[string][]MatrixValue

func (m *MatrixAxes) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("matrix must be a map of dimensions")
	}
	axes := make(MatrixAxes)
	for i := 0; i < len(value.Content); i += 2 {
		key := value.Content[i].Value
		node := value.Content[i+1]
		if node.Kind != yaml.SequenceNode {
			return fmt.Errorf("matrix dimension %q must be a list", key)
		}
		var values []MatrixValue
		for _, item := range node.Content {
			if item.Kind == yaml.ScalarNode {
				values = append(values, MatrixValue{Name: item.Value})
				continue
			}
			var parsed MatrixValue
			if err := item.Decode(&parsed); err != nil {
				return fmt.Errorf("matrix dimension %q: %w", key, err)
			}
			if parsed.Name == "" {
				return fmt.Errorf("matrix dimension %q has a value without a name", key)
			}
			values = append(values, parsed)
		}
		sort.SliceStable(values, func(a, b int) bool {
			return values[a].Priority < values[b].Priority
		})
		axes[key] = values
	}
	*m = axes
	return nil
}

type Coverage struct {
	Enabled   bool   `yaml:"enabled"`
	Threshold int    `yaml:"threshold"`
	File      string `yaml:"file,omitempty"`
	Format    string `yaml:"format,omitempty"`
}

type Service struct {
	Name        string            `yaml:"name"`
	Image       string            `yaml:"image"`
	Environment map[string]string `yaml:"environment,omitempty"`
	Ports       []string          `yaml:"ports,omitempty"`
}

type HookRef struct {
	Uses string `yaml:"uses"`
	When string `yaml:"when,omitempty"`
}

func (h *HookRef) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		h.Uses = value.Value
		return nil
	}
	var raw struct {
		Uses string `yaml:"uses"`
		When string `yaml:"when,omitempty"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	*h = HookRef{Uses: raw.Uses, When: raw.When}
	return nil
}

type Step struct {
	Uses string `yaml:"uses,omitempty"`
	Run  string `yaml:"run,omitempty"`
	When string `yaml:"when,omitempty"`
}

func (s *Step) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		s.Run = value.Value
		return nil
	}
	var raw struct {
		Uses string `yaml:"uses,omitempty"`
		Run  string `yaml:"run,omitempty"`
		When string `yaml:"when,omitempty"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	*s = Step{Uses: raw.Uses, Run: raw.Run, When: raw.When}
	return nil
}

// Action is a reusable body. A runner makes it a separate container.
// Without a runner, its commands are expanded into the calling stage.
type Action struct {
	Runner           string            `yaml:"runner,omitempty"`
	Commands         []string          `yaml:"commands,omitempty"`
	Shell            string            `yaml:"shell,omitempty"`
	Environment      map[string]string `yaml:"environment,omitempty"`
	Volumes          []lib.Volume      `yaml:"volumes,omitempty"`
	DockerSocket     bool              `yaml:"docker_socket,omitempty"`
	Once             bool              `yaml:"once,omitempty"`
	Timeout          string            `yaml:"timeout,omitempty"`
	WorkingDirectory string            `yaml:"working_directory,omitempty"`
}

type Stage struct {
	Runner           string            `yaml:"runner,omitempty"`
	Version          string            `yaml:"version,omitempty"`
	Commands         []string          `yaml:"commands,omitempty"`
	Shell            string            `yaml:"shell,omitempty"`
	Steps            []Step            `yaml:"steps,omitempty"`
	Before           []HookRef         `yaml:"before,omitempty"`
	After            []HookRef         `yaml:"after,omitempty"`
	Requires         []string          `yaml:"requires,omitempty"`
	Environment      map[string]string `yaml:"environment,omitempty"`
	Volumes          []lib.Volume      `yaml:"volumes,omitempty"`
	Artifacts        []string          `yaml:"artifacts,omitempty"`
	Coverage         *Coverage         `yaml:"coverage,omitempty"`
	Timeout          string            `yaml:"timeout,omitempty"`
	When             string            `yaml:"when,omitempty"`
	WorkingDirectory string            `yaml:"working_directory,omitempty"`
	DockerSocket     bool              `yaml:"docker_socket,omitempty"`
	Services         []Service         `yaml:"services,omitempty"`
	Matrix           []string          `yaml:"matrix,omitempty"`
}

// execVars holds variables passed during execution
type execVars map[string]string

// parseExecVars converts command line variables into a map
func parseExecVars(vars []string) execVars {
	result := make(execVars)
	for _, v := range vars {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) == 2 && parts[1] != "" {
			result[parts[0]] = parts[1]
		}
	}
	return result
}

// resolveVars replaces ${var} references in strings with their values
func resolveVars(s string, vars execVars) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "${"+k+"}", v)
	}
	return s
}

// resolveStageVars replaces variables in stage configuration
func resolveStageVars(stage *Stage, vars execVars) {
	// Resolve environment variables
	for k, v := range stage.Environment {
		stage.Environment[k] = resolveVars(v, vars)
	}

	// Resolve volume paths
	for i, vol := range stage.Volumes {
		stage.Volumes[i].Source = resolveVars(vol.Source, vars)
		stage.Volumes[i].Target = resolveVars(vol.Target, vars)
	}

	// Resolve commands
	for i, cmd := range stage.Commands {
		stage.Commands[i] = resolveVars(cmd, vars)
	}
}

func loadConfig(path string, vars execVars) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var config Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	// Then decode the full config
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}
	if vars != nil {
		// Resolve S3 bucket name
		if config.Audit.S3Bucket != "" {
			config.Audit.S3Bucket = resolveVars(config.Audit.S3Bucket, vars)
		}

		// Resolve AWS profile
		if config.Audit.AWSProfile != "" {
			config.Audit.AWSProfile = resolveVars(config.Audit.AWSProfile, vars)
		}

		// Resolve audit path
		if config.Audit.Path != "" {
			config.Audit.Path = resolveVars(config.Audit.Path, vars)
		}
	}
	applyRunnerAliases(&config, vars)
	if err := validateSuites(&config); err != nil {
		return nil, err
	}

	return &config, nil
}

// validateSuites checks that each suite is a non-empty list of distinct stages.
// A suite does not contain other suites, and its name cannot match a stage.
func validateSuites(config *Config) error {
	for _, name := range sortedSuiteNames(config.Suites) {
		if _, ok := config.Stages[name]; ok {
			return fmt.Errorf("suite %q has the same name as a stage", name)
		}
		members := config.Suites[name]
		if len(members) == 0 {
			return fmt.Errorf("suite %q has no stages", name)
		}
		seen := make(map[string]bool, len(members))
		for _, member := range members {
			if _, ok := config.Stages[member]; !ok {
				return fmt.Errorf("suite %q references unknown stage %q", name, member)
			}
			if seen[member] {
				return fmt.Errorf("suite %q lists stage %q more than once", name, member)
			}
			seen[member] = true
		}
	}
	return nil
}

func sortedSuiteNames(suites map[string][]string) []string {
	names := make([]string, 0, len(suites))
	for name := range suites {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type unknownTargetsError struct {
	names []string
}

func (e unknownTargetsError) Error() string {
	return "invalid stage(s): " + strings.Join(e.names, ", ")
}

// expandRunTargets replaces suite names with their stages. A stage is kept at
// its first position when a later suite or argument names it again.
func expandRunTargets(targets []string, suites map[string][]string, stages map[string]Stage) ([]string, error) {
	seen := make(map[string]bool, len(targets))
	var expanded []string
	var unknown []string
	for _, target := range targets {
		if members, ok := suites[target]; ok {
			for _, name := range members {
				if !seen[name] {
					seen[name] = true
					expanded = append(expanded, name)
				}
			}
			continue
		}
		if _, ok := stages[target]; ok {
			if !seen[target] {
				seen[target] = true
				expanded = append(expanded, target)
			}
			continue
		}
		unknown = append(unknown, target)
	}
	if len(unknown) > 0 {
		return nil, unknownTargetsError{names: unknown}
	}
	return expanded, nil
}

func resolveRunnerName(runner string, runners Runners, vars execVars) string {
	runner = resolveVars(runner, vars)
	if image, ok := runners[runner]; ok {
		return image
	}
	return runner
}

// applyRunnerAliases replaces a stage or action runner with its image when the
// runner name matches a configured alias. Any other runner string is left
// unchanged and is still treated as an image reference.
func applyRunnerAliases(config *Config, vars execVars) {
	for alias, image := range config.Runners {
		config.Runners[alias] = resolveVars(image, vars)
	}
	for name, action := range config.Actions {
		action.Runner = resolveRunnerName(action.Runner, config.Runners, vars)
		for i, cmd := range action.Commands {
			action.Commands[i] = resolveVars(cmd, vars)
		}
		action.WorkingDirectory = resolveVars(action.WorkingDirectory, vars)
		config.Actions[name] = action
	}
	for name, stage := range config.Stages {
		resolveStageVars(&stage, vars)
		stage.Runner = resolveRunnerName(stage.Runner, config.Runners, vars)
		stage.WorkingDirectory = resolveVars(stage.WorkingDirectory, vars)
		stage.When = resolveVars(stage.When, vars)
		for i, step := range stage.Steps {
			stage.Steps[i].Run = resolveVars(step.Run, vars)
			stage.Steps[i].When = resolveVars(step.When, vars)
		}
		config.Stages[name] = stage
	}
}

// VerifyRequirements checks if all required stages have been executed successfully
func verifyRequirements(stage Stage, auditStore lib.AuditStore, projectName, gitRevision string) error {
	if len(stage.Requires) == 0 {
		return nil
	}

	// Load audit logs for this project and revision
	logs, err := auditStore.LoadLogs(projectName, gitRevision)
	if err != nil {
		return fmt.Errorf("loading audit logs: %w", err)
	}

	// The newest log for a stage decides. A later failure must not be hidden
	// by an earlier success from another container in the same stage.
	latest := make(map[string]lib.AuditLog)
	for _, log := range logs {
		prev, ok := latest[log.Stage]
		if !ok || !log.StartTime.Before(prev.StartTime) {
			latest[log.Stage] = log
		}
	}
	successful := make(map[string]bool)
	for stageName, log := range latest {
		if log.Status == "success" || log.Status == "skipped" {
			successful[stageName] = true
		}
	}

	// Check each required stage
	var missing []string
	for _, req := range stage.Requires {
		if !successful[req] {
			missing = append(missing, req)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("required stages not completed successfully: %s", strings.Join(missing, ", "))
	}

	return nil
}

func createStageCommand(name string, stage Stage, config *Config) *cli.Command {
	return &cli.Command{
		Name:        name,
		Usage:       fmt.Sprintf("Run the %s stage", name),
		Description: fmt.Sprintf("Run the %s stage using %s runner", name, stage.Runner),
		Action: func(ctx *cli.Context) error {
			names, stages, err := expandMatrixStages([]string{name}, config, nil)
			if err != nil {
				return err
			}
			gate := &actionOnce{}
			for _, inst := range names {
				if err := runConfiguredStage(ctx, inst, stages[inst], config, nil, false, nil, nil, gate, nil, nil); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// ensureWorkspaceMount adds the default current-directory mount when the stage
// does not already bind something at /workspace. The returned stage does not
// share its volume slice with the caller.
func ensureWorkspaceMount(stage Stage) Stage {
	for _, vol := range stage.Volumes {
		if vol.Target == "/workspace" {
			return stage
		}
	}
	volumes := make([]lib.Volume, len(stage.Volumes), len(stage.Volumes)+1)
	copy(volumes, stage.Volumes)
	stage.Volumes = append(volumes, lib.Volume{
		Type:   "bind",
		Source: ".",
		Target: "/workspace",
	})
	return stage
}

// withoutRequires returns a copy of stage that drops requirements named in skip.
// Parallel runs use this so in-invocation dependencies are enforced by the
// scheduler, not by audit logs written while a stage is still running.
func withoutRequires(stage Stage, skip map[string]bool) Stage {
	if len(skip) == 0 || len(stage.Requires) == 0 {
		return stage
	}
	kept := make([]string, 0, len(stage.Requires))
	for _, req := range stage.Requires {
		if !skip[req] {
			kept = append(kept, req)
		}
	}
	stage.Requires = kept
	return stage
}

func executeStage(ctx *cli.Context, name string, stage Stage, config *Config, skipRequires map[string]bool, prefixOutput bool, outputMu *sync.Mutex, auditStore lib.AuditStore, runCtx context.Context) error {
	var err error
	if auditStore == nil {
		auditStore, err = createAuditStore(config, ctx)
		if err != nil {
			return fmt.Errorf("creating audit store: %w", err)
		}
	}

	gitRev, err := getGitRevision()
	if err != nil {
		gitRev = "unknown" // Don't fail if we can't get git revision
	}

	stage = ensureWorkspaceMount(stage)
	if err := verifyRequirements(withoutRequires(stage, skipRequires), auditStore, config.Project.Name, gitRev); err != nil {
		return fmt.Errorf("stage requirements not met: %w", err)
	}

	timeout, err := time.ParseDuration(stage.Timeout)
	if stage.Timeout != "" && err != nil {
		return fmt.Errorf("invalid timeout %q: %w", stage.Timeout, err)
	}
	if stage.Timeout == "" {
		timeout = 0
	}

	services := make([]lib.ServiceSpec, len(stage.Services))
	for i, svc := range stage.Services {
		services[i] = lib.ServiceSpec{
			Name:        svc.Name,
			Image:       lib.ResolveRunnerImage(svc.Image, defaultRegistry),
			Environment: svc.Environment,
			Ports:       svc.Ports,
		}
	}

	stageExec := lib.StageExecution{
		Name:             name,
		Runner:           lib.ResolveRunnerImage(stage.Runner, defaultRegistry),
		Commands:         stage.Commands,
		Shell:            stage.Shell,
		Environment:      stage.Environment,
		Volumes:          stage.Volumes,
		Verbose:          ctx.Bool("verbose"),
		PrefixOutput:     prefixOutput,
		OutputLock:       outputMu,
		WorkingDirectory: stage.WorkingDirectory,
		Timeout:          timeout,
		DockerSocket:     stage.DockerSocket,
		Services:         services,
		Context:          runCtx,
	}

	return lib.ExecuteStage(stageExec, auditStore, config.Project.Name)
}

// orderStageNames lists stages so a required stage comes before the stages
// that name it. Stages with no remaining dependencies are ordered by name.
func orderStageNames(stages map[string]Stage) []string {
	names := make([]string, 0, len(stages))
	for name := range stages {
		names = append(names, name)
	}
	sort.Strings(names)

	indegree := make(map[string]int, len(names))
	dependents := make(map[string][]string, len(names))
	for _, name := range names {
		for _, req := range stages[name].Requires {
			if _, ok := stages[req]; !ok {
				continue
			}
			dependents[req] = append(dependents[req], name)
			indegree[name]++
		}
	}

	var ready []string
	for _, name := range names {
		if indegree[name] == 0 {
			ready = append(ready, name)
		}
	}
	var ordered []string
	for len(ready) > 0 {
		name := ready[0]
		ready = ready[1:]
		ordered = append(ordered, name)
		next := append([]string{}, dependents[name]...)
		sort.Strings(next)
		for _, dep := range next {
			indegree[dep]--
			if indegree[dep] == 0 {
				ready = insertSorted(ready, dep)
			}
		}
	}
	if len(ordered) == len(names) {
		return ordered
	}
	seen := make(map[string]bool, len(ordered))
	for _, name := range ordered {
		seen[name] = true
	}
	for _, name := range names {
		if !seen[name] {
			ordered = append(ordered, name)
		}
	}
	return ordered
}

func insertSorted(names []string, name string) []string {
	i := sort.SearchStrings(names, name)
	names = append(names, "")
	copy(names[i+1:], names[i:])
	names[i] = name
	return names
}

func hookNames(hooks []HookRef) []string {
	names := make([]string, 0, len(hooks))
	for _, hook := range hooks {
		if hook.Uses != "" {
			names = append(names, hook.Uses)
		}
	}
	return names
}

func stageCommandUsage(name string, stage Stage) string {
	usage := fmt.Sprintf("Run the %s stage", name)
	if reqs := strings.Join(stage.Requires, ", "); reqs != "" {
		usage += "; requires " + reqs
	}
	if hooks := strings.Join(hookNames(stage.Before), ", "); hooks != "" {
		usage += "; before " + hooks
	}
	return usage
}

func formatStageHelp(suites map[string][]string, stages map[string]Stage) string {
	var b strings.Builder
	if len(suites) > 0 {
		b.WriteString("Suites:")
		names := sortedSuiteNames(suites)
		width := 0
		for _, name := range names {
			if len(name) > width {
				width = len(name)
			}
		}
		for _, name := range names {
			fmt.Fprintf(&b, "\n  %-*s  %s", width, name, strings.Join(suites[name], ", "))
		}
	}
	if len(stages) == 0 {
		return b.String()
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString("Stages, dependencies first:")
	for _, name := range orderStageNames(stages) {
		stage := stages[name]
		fmt.Fprintf(&b, "\n  %s", name)
		if len(stage.Requires) > 0 {
			fmt.Fprintf(&b, "\n    requires: %s", strings.Join(stage.Requires, ", "))
		}
		if hooks := hookNames(stage.Before); len(hooks) > 0 {
			fmt.Fprintf(&b, "\n    before: %s", strings.Join(hooks, ", "))
		}
	}
	return b.String()
}

func run(args []string) error {
	cliApp := cli.NewApp()
	cliApp.Name = "gosonic"
	cliApp.Usage = "A build tool for CI/CD pipelines"
	cliApp.Description = "Gosonic provides a unified way to build, test, package and deploy applications"

	// Global flags
	cliApp.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:    "sonic-file",
			Aliases: []string{"f"},
			Value:   defaultConfigFile,
			Usage:   "Path to sonic configuration file",
			EnvVars: []string{"SONIC_CONFIG_FILE"},
		},
		&cli.StringSliceFlag{
			Name:    "var",
			Aliases: []string{"v"},
			Usage:   "Execution variables in key=value format (can be specified multiple times)",
			EnvVars: []string{"SONIC_VARS"},
		},
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
		&cli.StringFlag{
			Name:    "registry",
			Usage:   "Default Docker registry to use when not specified in image reference",
			Value:   defaultRegistry,
			EnvVars: []string{"GOSONIC_DEFAULT_REGISTRY"},
		},
		&cli.BoolFlag{
			Name:    "verbose",
			Usage:   "Enable verbose output",
			Value:   false,
			EnvVars: []string{"GOSONIC_VERBOSE"},
		},
	}

	// Load config and create commands immediately
	var configPath string
	var vars execVars
	for i, arg := range args {
		if (arg == "--sonic-file" || arg == "-f") && i+1 < len(args) {
			configPath = args[i+1]
		}
		if strings.HasPrefix(arg, "--sonic-file=") {
			configPath = strings.TrimPrefix(arg, "--sonic-file=")
		}
		if arg == "--var" && i+1 < len(args) {
			if vars == nil {
				vars = make(execVars)
			}
			parts := strings.SplitN(args[i+1], "=", 2)
			if len(parts) == 2 && parts[1] != "" {
				vars[parts[0]] = parts[1]
			}
		}
	}
	configPath = resolveConfigPath(configPath)

	// Start with built-in commands
	commands := []*cli.Command{
		{
			Name:    "help",
			Aliases: []string{"h"},
			Usage:   "Shows a list of commands or help for one command",
			Action: func(ctx *cli.Context) error {
				args := ctx.Args()
				if args.Present() {
					return cli.ShowCommandHelp(ctx, args.First())
				}
				return cli.ShowAppHelp(ctx)
			},
		},
	}

	// Try to load config and add stage commands
	var config *Config
	var err error
	config, err = loadConfig(configPath, vars)
	if err != nil {
		config = &Config{} // Use empty config if loading fails
	}

	// Add the run command after config is loaded
	commands = append(commands, &cli.Command{
		Name:        "run",
		Usage:       "Run one or more stages in order, or in parallel with --jobs",
		Description: formatStageHelp(config.Suites, config.Stages),
		Flags: []cli.Flag{
			&cli.IntFlag{
				Name:    "jobs",
				Aliases: []string{"j"},
				Value:   1,
				Usage:   "Maximum number of stages to run at once. 1 runs stages in the listed order",
			},
			&cli.StringSliceFlag{
				Name:  "matrix",
				Usage: "Override a matrix dimension as key=v1,v2",
			},
		},
		Action: func(ctx *cli.Context) error {
			if !ctx.Args().Present() {
				cli.HelpPrinter(ctx.App.Writer, cli.CommandHelpTemplate, ctx.Command)
				return nil
			}

			jobs := ctx.Int("jobs")
			if jobs < 1 {
				return fmt.Errorf("--jobs must be at least 1")
			}

			// Suites expand to their stages before matrix cells and scheduling.
			stages, err := expandRunTargets(ctx.Args().Slice(), config.Suites, config.Stages)
			if err != nil {
				if unknown, ok := err.(unknownTargetsError); ok {
					fmt.Fprintf(os.Stderr, "Error: invalid stage(s): %s\n\n", strings.Join(unknown.names, ", "))
					if len(config.Suites) > 0 {
						fmt.Fprintf(os.Stderr, "Available suites:\n")
						for _, name := range sortedSuiteNames(config.Suites) {
							fmt.Fprintf(os.Stderr, "  %s - %s\n", name, strings.Join(config.Suites[name], ", "))
						}
						fmt.Fprintln(os.Stderr)
					}
					fmt.Fprintf(os.Stderr, "Available stages:\n")
					for _, name := range orderStageNames(config.Stages) {
						fmt.Fprintf(os.Stderr, "  %s - %s\n", name, config.Stages[name].Runner)
					}
					return fmt.Errorf("invalid stage(s) specified")
				}
				return err
			}

			overrides, err := parseMatrixOverrides(ctx.StringSlice("matrix"))
			if err != nil {
				return err
			}
			if err := validateActionRefs(config); err != nil {
				return err
			}
			runNames, runStages, err := expandMatrixStages(stages, config, overrides)
			if err != nil {
				return err
			}

			gate := &actionOnce{}
			progressSet := lib.NewProgressSet(ctx.Bool("verbose"), jobs > 1)
			defer progressSet.Close()
			// jobs == 1 keeps command-line order and does not use the scheduler.
			if jobs == 1 {
				for _, name := range runNames {
					if err := runConfiguredStage(ctx, name, runStages[name], config, nil, false, nil, nil, gate, progressSet, nil); err != nil {
						return fmt.Errorf("stage %q failed: %w", name, err)
					}
				}
				return nil
			}

			inRun := make(map[string]bool, len(runNames))
			for _, name := range runNames {
				inRun[name] = true
			}

			auditStore, err := createAuditStore(config, ctx)
			if err != nil {
				return fmt.Errorf("creating audit store: %w", err)
			}
			gitRev, err := getGitRevision()
			if err != nil {
				gitRev = "unknown"
			}
			for _, name := range runNames {
				if err := verifyRequirements(withoutRequires(runStages[name], inRun), auditStore, config.Project.Name, gitRev); err != nil {
					return fmt.Errorf("stage %q failed: %w", name, fmt.Errorf("stage requirements not met: %w", err))
				}
			}

			var outputMu sync.Mutex
			return scheduleStages(runNames, runStages, jobs, func(runCtx context.Context, name string) error {
				return runConfiguredStage(ctx, name, runStages[name], config, inRun, true, &outputMu, auditStore, gate, progressSet, runCtx)
			})
		},
	})

	// Add stage commands for help display, dependencies first.
	if err == nil {
		for _, name := range orderStageNames(config.Stages) {
			stage := config.Stages[name]
			commands = append(commands, &cli.Command{
				Name:        name,
				Usage:       stageCommandUsage(name, stage),
				Description: fmt.Sprintf("Run the %s stage using %s runner", name, stage.Runner),
				Action: func(ctx *cli.Context) error {
					cmd := createStageCommand(name, stage, config)
					return cmd.Run(ctx)
				},
			})
		}
	}

	// Add this to the commands slice in the run function
	commands = append(commands, &cli.Command{
		Name:  "verify-aws-creds",
		Usage: "Verify AWS credentials for S3 audit logging",
		Action: func(ctx *cli.Context) error {

			fmt.Println("Verifying AWS credentials for S3 audit logging...")

			// Create S3 client
			client, err := createS3Client(true) // Always verbose for this command
			if err != nil {
				return fmt.Errorf("creating S3 client: %w", err)
			}

			// Get bucket name
			bucket := ctx.String("audit-s3-bucket")
			if bucket == "" {
				bucket = config.Audit.S3Bucket
			}
			if bucket == "" {
				return fmt.Errorf("s3 bucket must be specified for verification")
			}

			// Try to list objects in the bucket to verify permissions
			fmt.Printf("Testing S3 access to bucket: %s\n", bucket)
			_, err = client.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{
				Bucket:  aws.String(bucket),
				MaxKeys: aws.Int32(1),
			})

			if err != nil {
				fmt.Printf("Error accessing S3 bucket: %v\n", err)
				fmt.Println("\nTroubleshooting steps:")
				fmt.Println("1. Check if the bucket exists and you have permission to access it")
				fmt.Println("2. Verify your AWS credentials are correct")
				fmt.Println("3. Check if the bucket is in the correct region")
				return fmt.Errorf("S3 access test failed: %w", err)
			}

			fmt.Println("AWS credentials verified successfully!")
			fmt.Println("S3 bucket access test passed.")
			return nil
		},
	})

	cliApp.Commands = commands

	// Add default action to handle multiple commands
	cliApp.Action = func(ctx *cli.Context) error {
		if !ctx.Args().Present() {
			return cli.ShowAppHelp(ctx)
		}

		// Get all commands to run
		cmdNames := ctx.Args().Slice()
		if len(cmdNames) == 1 {
			// Single command - let the CLI framework handle it
			return cli.ShowAppHelp(ctx)
		}

		// Multiple commands - run them in sequence
		for _, name := range cmdNames {
			for _, cmd := range commands {
				if cmd.Name == name {
					if err := cmd.Run(ctx); err != nil {
						return fmt.Errorf("stage %q failed: %w", name, err)
					}
					break
				}
			}
		}
		return nil
	}

	if err := cliApp.Run(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		if os.Getenv("GO_TEST") != "1" {
			os.Exit(1)
		}
		return err
	}
	return nil
}

func main() {
	run(os.Args)
}
