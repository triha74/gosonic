package lib

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Volume represents a docker volume mount configuration
type Volume struct {
	Type     string `yaml:"type"`               // bind, cache, tmp
	Source   string `yaml:"source"`             // host path or named volume
	Target   string `yaml:"target"`             // container path
	Readonly bool   `yaml:"readonly,omitempty"` // mount as readonly
}

type DockerResult struct {
	Stdout   string
	Stderr   string
	Error    error
	ExitCode int
}

// execDockerImpl is the actual implementation
func execDockerImpl(args []string) DockerResult {
	cmd := exec.Command(args[0], args[1:]...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	return DockerResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Error:    err,
		ExitCode: exitCode,
	}
}

// ExecDocker is a variable that can be overridden in tests
var ExecDocker = execDockerImpl

// ImageRef represents a parsed Docker image reference
type ImageRef struct {
	Domain      string // e.g., "docker.io", "public.ecr.aws"
	ContextPath string // e.g., "library", "docker/library"
	Name        string // e.g., "golang"
	Tag         string // e.g., "1.22"
	Digest      string // e.g., "sha256:123..."
}

// ParseImageRef parses a Docker image reference into its components
func ParseImageRef(ref string) ImageRef {
	// Handle empty case
	if ref == "" {
		return ImageRef{}
	}

	var image ImageRef

	// Split domain and rest
	parts := strings.Split(ref, "/")

	// Check if first part is a domain
	if len(parts) > 1 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":")) {
		image.Domain = parts[0]
		parts = parts[1:]
	}

	// Last part contains the image name and tag/digest
	if len(parts) > 0 {
		lastPart := parts[len(parts)-1]

		// Handle name, tag and digest
		nameAndDigest := strings.Split(lastPart, "@")

		// First handle the name and tag part
		nameAndTag := strings.Split(nameAndDigest[0], ":")
		image.Name = nameAndTag[0]
		if len(nameAndTag) > 1 {
			image.Tag = nameAndTag[1]
		}

		// Then handle the digest if present
		if len(nameAndDigest) > 1 {
			image.Digest = nameAndDigest[1]
		}

		// Everything else is context path
		if len(parts) > 1 {
			image.ContextPath = strings.Join(parts[:len(parts)-1], "/")
		}
	}

	return image
}

// String returns the full image reference
func (i ImageRef) String() string {
	var parts []string

	// Add domain if present
	if i.Domain != "" {
		parts = append(parts, i.Domain)
	}

	// Add context path if present
	if i.ContextPath != "" {
		parts = append(parts, i.ContextPath)
	}

	// Add name
	parts = append(parts, i.Name)

	// Join all parts
	ref := strings.Join(parts, "/")

	// Add tag or digest
	if i.Tag != "" {
		ref += ":" + i.Tag
	}
	if i.Digest != "" {
		ref += "@" + i.Digest
	}

	return ref
}

// ResolveRunnerImage returns the full Docker image path for a runner
// If no domain is specified, it uses the provided default registry
func ResolveRunnerImage(runner string, defaultRegistry string) string {
	if runner == "" {
		return defaultRegistry + "/docker/library/alpine:latest"
	}

	// Parse the image reference
	ref := ParseImageRef(runner)

	// If no domain is specified, use the default registry
	if ref.Domain == "" {
		ref.Domain = defaultRegistry
	}

	return ref.String()
}

// StageExecution represents the configuration needed to execute a stage
type StageExecution struct {
	Name        string
	Runner      string
	Commands    []string
	Environment map[string]string
	Volumes     []Volume
}

// ExecuteStage runs a stage in a docker container and handles audit logging
func ExecuteStage(stage StageExecution, auditStore AuditStore, projectName string) error {
	startTime := time.Now()

	// Get git revision
	gitRev, err := GetGitRevision()
	if err != nil {
		gitRev = "unknown" // Don't fail if we can't get git revision
	}

	// Build docker command
	dockerArgs := []string{
		"docker", "run",
		"--rm",                    // Remove container after execution
		"--init",                  // Use tini as init process
		"--workdir", "/workspace", // Set working directory
	}

	// Add environment variables
	for k, v := range stage.Environment {
		dockerArgs = append(dockerArgs, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	// Add volume mounts
	for _, vol := range stage.Volumes {
		mountOpts := []string{}
		if vol.Readonly {
			mountOpts = append(mountOpts, "ro")
		}

		volumeArg := fmt.Sprintf("%s:%s", vol.Source, vol.Target)
		if len(mountOpts) > 0 {
			volumeArg += ":" + strings.Join(mountOpts, ",")
		}
		dockerArgs = append(dockerArgs, "-v", volumeArg)
	}

	// Add image name
	dockerArgs = append(dockerArgs, stage.Runner)

	// Add commands
	if len(stage.Commands) == 1 {
		// For a single command, execute directly without shell
		args := splitCommandArgs(stage.Commands[0])
		dockerArgs = append(dockerArgs, args...)
	} else if len(stage.Commands) > 1 {
		// For multiple commands, use shell
		command := strings.Join(stage.Commands, " && ")
		dockerArgs = append(dockerArgs, "sh", "-c", command)
	}

	// Create the full command string for audit
	fullCommand := strings.Join(dockerArgs, " ")

	// Print the command
	fmt.Printf("Stage: %s\n", stage.Name)
	fmt.Printf("Runner: %s\n", stage.Runner)
	fmt.Printf("\nDocker command:\n%s\n", fullCommand)

	// Create audit log
	auditLog := AuditLog{
		Project:     projectName,
		GitRevision: gitRev,
		Stage:       stage.Name,
		Command:     fullCommand,
		StartTime:   startTime,
		Status:      "success", // Will be updated if there's an error
	}

	// Write initial audit log
	if auditStore != nil {
		if err := auditStore.Store(auditLog); err != nil {
			fmt.Printf("Error writing audit log: %v\n", err)
		}
	}

	// Execute docker command
	result := ExecDocker(dockerArgs)

	// Print output
	if result.Stdout != "" {
		fmt.Println(result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Printf("%s", result.Stderr)
	}

	// Update audit log if there was an error
	if result.Error != nil {
		auditLog.SetError(result.Error)
		if auditStore != nil {
			if err := auditStore.Store(auditLog); err != nil {
				fmt.Printf("Error writing audit log: %v\n", err)
			}
		}
		return result.Error
	}

	return nil
}

// splitCommandArgs splits a command string into arguments, respecting quotes
func splitCommandArgs(cmd string) []string {
	var args []string
	var currentArg strings.Builder
	inQuotes := false
	quoteChar := rune(0)

	for _, char := range cmd {
		switch {
		case char == '"' || char == '\'':
			if inQuotes && char == quoteChar {
				// End of quoted section
				inQuotes = false
				quoteChar = rune(0)
			} else if !inQuotes {
				// Start of quoted section
				inQuotes = true
				quoteChar = char
			} else {
				// Quote character inside another quote type
				currentArg.WriteRune(char)
			}
		case char == ' ' && !inQuotes:
			// Space outside quotes - end of argument
			if currentArg.Len() > 0 {
				args = append(args, currentArg.String())
				currentArg.Reset()
			}
		default:
			// Regular character
			currentArg.WriteRune(char)
		}
	}

	// Add the last argument if there is one
	if currentArg.Len() > 0 {
		args = append(args, currentArg.String())
	}

	return args
}
