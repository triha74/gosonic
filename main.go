package main

import (
	"bytes"
	"context"
	"fmt"
	"gosonic/lib"
	"os"
	"strings"
	"sync"

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
	defaultConfigFile = ".sonic.yml"
	defaultRegistry   = "public.ecr.aws"
	defaultRunner     = "public.ecr.aws/docker/library/alpine:latest"
	defaultAuditStore = "file"
)

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
	Runners Runners          `yaml:"runners,omitempty"`
	Stages  map[string]Stage `yaml:"stages"`
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

type Matrix struct {
	Region []MatrixValue `yaml:"region,omitempty"`
	// Can add more dimensions like environment, platform, etc.
}

type Stage struct {
	Runner      string            `yaml:"runner"`
	Version     string            `yaml:"version,omitempty"`
	Commands    []string          `yaml:"commands,omitempty"`
	Requires    []string          `yaml:"requires,omitempty"`
	Environment map[string]string `yaml:"environment,omitempty"`
	Volumes     []lib.Volume      `yaml:"volumes,omitempty"`
	Artifacts   []string          `yaml:"artifacts,omitempty"`
	Coverage    *struct {
		Enabled   bool `yaml:"enabled"`
		Threshold int  `yaml:"threshold"`
	} `yaml:"coverage,omitempty"`
	Timeout string `yaml:"timeout,omitempty"`
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

	return &config, nil
}

// applyRunnerAliases replaces a stage runner with its image when the runner
// name matches a configured alias. Any other runner string is left unchanged
// and is still treated as an image reference.
func applyRunnerAliases(config *Config, vars execVars) {
	for alias, image := range config.Runners {
		config.Runners[alias] = resolveVars(image, vars)
	}
	for name, stage := range config.Stages {
		resolveStageVars(&stage, vars)
		stage.Runner = resolveVars(stage.Runner, vars)
		if image, ok := config.Runners[stage.Runner]; ok {
			stage.Runner = image
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

	// Create a map of successful stages
	successful := make(map[string]bool)
	for _, log := range logs {
		if log.Status == "success" {
			successful[log.Stage] = true
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
			return executeStage(ctx, name, stage, config, nil, false, nil, nil)
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

func executeStage(ctx *cli.Context, name string, stage Stage, config *Config, skipRequires map[string]bool, prefixOutput bool, outputMu *sync.Mutex, auditStore lib.AuditStore) error {
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

	stageExec := lib.StageExecution{
		Name:         name,
		Runner:       lib.ResolveRunnerImage(stage.Runner, defaultRegistry),
		Commands:     stage.Commands,
		Environment:  stage.Environment,
		Volumes:      stage.Volumes,
		Verbose:      ctx.Bool("verbose"),
		PrefixOutput: prefixOutput,
		OutputLock:   outputMu,
	}

	return lib.ExecuteStage(stageExec, auditStore, config.Project.Name)
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
	configPath := defaultConfigFile
	var vars execVars
	for i, arg := range args {
		if arg == "--sonic-file" && i+1 < len(args) {
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
		Name:  "run",
		Usage: "Run one or more stages in order, or in parallel with --jobs",
		Flags: []cli.Flag{
			&cli.IntFlag{
				Name:    "jobs",
				Aliases: []string{"j"},
				Value:   1,
				Usage:   "Maximum number of stages to run at once. 1 runs stages in the listed order",
			},
		},
		Action: func(ctx *cli.Context) error {
			if !ctx.Args().Present() {
				return fmt.Errorf("no stages specified")
			}

			jobs := ctx.Int("jobs")
			if jobs < 1 {
				return fmt.Errorf("--jobs must be at least 1")
			}

			// Get stages to run
			stages := ctx.Args().Slice()

			// Validate all stages before executing any
			var invalidStages []string
			for _, name := range stages {
				if _, ok := config.Stages[name]; !ok {
					invalidStages = append(invalidStages, name)
				}
			}

			if len(invalidStages) > 0 {
				fmt.Fprintf(os.Stderr, "Error: invalid stage(s): %s\n\n", strings.Join(invalidStages, ", "))
				fmt.Fprintf(os.Stderr, "Available stages:\n")
				for name := range config.Stages {
					fmt.Fprintf(os.Stderr, "  %s - %s\n", name, config.Stages[name].Runner)
				}
				return fmt.Errorf("invalid stage(s) specified")
			}

			// jobs == 1 keeps command-line order and does not use the scheduler.
			if jobs == 1 {
				for _, name := range stages {
					stage := config.Stages[name]
					cmd := createStageCommand(name, stage, config)
					if err := cmd.Run(ctx); err != nil {
						return fmt.Errorf("stage %q failed: %w", name, err)
					}
				}
				return nil
			}

			inRun := make(map[string]bool, len(stages))
			for _, name := range stages {
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
			for _, name := range stages {
				if err := verifyRequirements(withoutRequires(config.Stages[name], inRun), auditStore, config.Project.Name, gitRev); err != nil {
					return fmt.Errorf("stage %q failed: %w", name, fmt.Errorf("stage requirements not met: %w", err))
				}
			}

			var outputMu sync.Mutex
			return scheduleStages(stages, config.Stages, jobs, func(name string) error {
				return executeStage(ctx, name, config.Stages[name], config, inRun, true, &outputMu, auditStore)
			})
		},
	})

	// Add stage commands for help display
	if err == nil {
		for name := range config.Stages {
			stage := config.Stages[name]
			commands = append(commands, &cli.Command{
				Name:        name,
				Usage:       fmt.Sprintf("Run the %s stage", name),
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
