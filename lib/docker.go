package lib

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
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

// ExecDocker is a variable that can be overridden in tests
var ExecDocker = execDockerImpl

// ExecDockerCtx runs docker with a context. Timeout handling uses this so a
// deadline cancels the container. Tests that only replace ExecDocker keep the
// non-timeout path.
var ExecDockerCtx = runDocker

func execDockerImpl(args []string) DockerResult {
	return runDocker(context.Background(), args)
}

func runDocker(ctx context.Context, args []string) DockerResult {
	if len(args) == 0 {
		return DockerResult{Error: fmt.Errorf("docker command is empty"), ExitCode: 1}
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("timed out: %w", ctx.Err())
	}
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if exitCode == 0 {
			exitCode = 1
		}
	}

	return DockerResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Error:    err,
		ExitCode: exitCode,
	}
}

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

	// Check if first part is a domain. localhost has no dot, but it is a
	// registry host for images built on this machine.
	if len(parts) > 1 && (parts[0] == "localhost" || strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":")) {
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

// ServiceSpec is a container started beside a stage and removed afterwards.
type ServiceSpec struct {
	Name        string
	Image       string
	Environment map[string]string
	Ports       []string
}

// StageExecution represents the configuration needed to execute a stage
type StageExecution struct {
	Name             string
	Runner           string
	Commands         []string
	Environment      map[string]string
	Volumes          []Volume
	Verbose          bool
	PrefixOutput     bool
	OutputLock       *sync.Mutex
	WorkingDirectory string
	Timeout          time.Duration
	DockerSocket     bool
	Services         []ServiceSpec
}

// ExecuteStage runs a stage in a docker container and handles audit logging
func ExecuteStage(stage StageExecution, auditStore AuditStore, projectName string) error {
	startTime := time.Now()

	// Get git revision
	gitRev, err := GetGitRevision()
	if err != nil {
		gitRev = "unknown" // Don't fail if we can't get git revision
	}

	network, cleanup, err := startServices(stage)
	if err != nil {
		return err
	}
	defer cleanup()

	dockerArgs, err := buildDockerArgs(stage, network)
	if err != nil {
		return err
	}

	// A single simple command is the container entrypoint. Multiple commands,
	// or a command that needs a shell, run under sh -c. Variable expansion
	// happens in that shell so container environment (including matrix
	// values) is visible. Sonic ${vars} are already resolved at load time.
	if len(stage.Commands) == 1 && !strings.Contains(stage.Commands[0], "\n") {
		dockerArgs = append(dockerArgs, splitCommandArgs(stage.Commands[0])...)
	} else if len(stage.Commands) > 0 {
		dockerArgs = append(dockerArgs, "sh", "-c", strings.Join(stage.Commands, " && "))
	}

	// Create the full command string for audit
	fullCommand := strings.Join(dockerArgs, " ")

	// Print the command only if verbose is enabled
	if stage.Verbose {
		stage.printf("Stage: %s\nRunner: %s\n\nDocker command:\n%s\n", stage.Name, stage.Runner, fullCommand)
	}

	// Create audit log. Status stays "running" until the container exits so a
	// concurrent stage cannot treat this record as a finished requirement.
	auditLog := AuditLog{
		Project:     projectName,
		GitRevision: gitRev,
		Stage:       stage.Name,
		Command:     fullCommand,
		StartTime:   startTime,
		Status:      "running",
	}

	// Write initial audit log
	if auditStore != nil {
		if err := auditStore.Store(auditLog); err != nil {
			// Always print audit log errors, even in non-verbose mode
			stage.printf("Error writing audit log: %v\n", err)
		}
	}

	// Execute docker command
	var result DockerResult
	if stage.Timeout > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), stage.Timeout)
		defer cancel()
		result = ExecDockerCtx(ctx, dockerArgs)
	} else {
		result = ExecDocker(dockerArgs)
	}
	if result.Error != nil && hostMountDenied(result) {
		stage.printf("Docker cannot mount the workspace from this host; copying it into the container.\n")
		result = runWithCopiedBinds(stage, dockerArgs)
	}

	// Print output - always show command output regardless of verbose setting
	stage.writeOutput(result.Stdout, result.Stderr)

	auditLog.Duration = time.Since(startTime).Seconds()
	if result.Error != nil {
		auditLog.SetError(result.Error)
	} else {
		auditLog.Status = "success"
	}
	if auditStore != nil {
		if err := auditStore.Store(auditLog); err != nil {
			stage.printf("Error writing audit log: %v\n", err)
		}
	}
	return result.Error
}

// printf writes a formatted message. Parallel stages share OutputLock and, when
// requested, prefix each line with the stage name.
func (stage StageExecution) printf(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	if stage.OutputLock != nil {
		stage.OutputLock.Lock()
		defer stage.OutputLock.Unlock()
	}
	if stage.PrefixOutput {
		writePrefixed(os.Stdout, stage.Name, text)
		return
	}
	fmt.Print(text)
}

// writeOutput prints command output. Sequential runs keep the historical
// stdout/stderr formatting. Parallel runs prefix every line and share OutputLock
// so stages do not interleave mid-line.
func (stage StageExecution) writeOutput(stdout, stderr string) {
	if stage.OutputLock != nil {
		stage.OutputLock.Lock()
		defer stage.OutputLock.Unlock()
	}
	if stage.PrefixOutput {
		writePrefixed(os.Stdout, stage.Name, stdout)
		writePrefixed(os.Stderr, stage.Name, stderr)
		return
	}
	if stdout != "" {
		fmt.Println(stdout)
	}
	if stderr != "" {
		fmt.Printf("%s", stderr)
	}
}

func writePrefixed(w io.Writer, name, text string) {
	if text == "" {
		return
	}
	lines := strings.Split(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for _, line := range lines {
		fmt.Fprintf(w, "%s | %s\n", name, line)
	}
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

func buildDockerArgs(stage StageExecution, network string) ([]string, error) {
	workdir := stage.WorkingDirectory
	if workdir == "" {
		workdir = "/workspace"
	}
	dockerArgs := []string{
		"docker", "run",
		"--rm",
		"--init",
		"--workdir", workdir,
	}
	if network != "" {
		dockerArgs = append(dockerArgs, "--network", network)
	}

	for k, v := range stage.Environment {
		dockerArgs = append(dockerArgs, "-e", fmt.Sprintf("%s=%s", k, v))
		if val, exists := os.LookupEnv(v); exists {
			dockerArgs = append(dockerArgs, "-e", fmt.Sprintf("%s=%s", v, val))
		}
	}

	volumes := append([]Volume{}, stage.Volumes...)
	if stage.DockerSocket {
		volumes = append(volumes, Volume{
			Type:   "bind",
			Source: "/var/run/docker.sock",
			Target: "/var/run/docker.sock",
		})
	}
	for _, vol := range volumes {
		arg, err := vol.dockerVolumeArg()
		if err != nil {
			return nil, err
		}
		dockerArgs = append(dockerArgs, "-v", arg)
	}

	dockerArgs = append(dockerArgs, stage.Runner)
	return dockerArgs, nil
}

func (v Volume) dockerVolumeArg() (string, error) {
	switch v.Type {
	case "", "bind":
		if v.Source == "" || v.Target == "" {
			return "", fmt.Errorf("bind volume requires source and target")
		}
		arg := fmt.Sprintf("%s:%s", v.Source, v.Target)
		if v.Readonly {
			arg += ":ro"
		}
		return arg, nil
	case "cache":
		if v.Source == "" || v.Target == "" {
			return "", fmt.Errorf("cache volume requires source and target")
		}
		arg := fmt.Sprintf("%s:%s", cacheVolumeName(v.Source), v.Target)
		if v.Readonly {
			arg += ":ro"
		}
		return arg, nil
	case "tmp":
		if v.Target == "" {
			return "", fmt.Errorf("tmp volume requires target")
		}
		return v.Target, nil
	default:
		return "", fmt.Errorf("unknown volume type %q", v.Type)
	}
}

func hostMountDenied(result DockerResult) bool {
	text := result.Stderr
	if result.Error != nil {
		text += "\n" + result.Error.Error()
	}
	return strings.Contains(text, "mounts denied") || strings.Contains(text, "not shared from the host")
}

// copyWorkspaceToVolume streams the working directory into a named volume.
// Tests replace it. The host target directory is left behind; it is a build
// cache, not source, and can be larger than the daemon can usefully receive.
var copyWorkspaceToVolume = streamWorkspaceToVolume

func runWithCopiedBinds(stage StageExecution, dockerArgs []string) DockerResult {
	volume := fmt.Sprintf("gosonic-ws-%d", time.Now().UnixNano())
	if result := ExecDocker([]string{"docker", "volume", "create", volume}); result.Error != nil {
		return result
	}
	defer ExecDocker([]string{"docker", "volume", "rm", "-f", volume})

	target := "/workspace"
	for _, vol := range stage.Volumes {
		if vol.Source == "." || vol.Source == "./" {
			if vol.Target != "" {
				target = vol.Target
			}
		}
	}
	if result := copyWorkspaceToVolume(stage.Runner, volume, target); result.Error != nil {
		return result
	}
	return ExecDocker(swapRelativeBinds(dockerArgs, volume))
}

func swapRelativeBinds(args []string, volume string) []string {
	swapped := append([]string{}, args...)
	for i := 0; i < len(swapped)-1; i++ {
		if swapped[i] != "-v" {
			continue
		}
		source, target, ok := splitBind(swapped[i+1])
		if ok && (source == "." || strings.HasPrefix(source, "./")) {
			swapped[i+1] = volume + ":" + target
		}
	}
	return swapped
}

func streamWorkspaceToVolume(image, volume, target string) DockerResult {
	wd, err := os.Getwd()
	if err != nil {
		return DockerResult{Error: fmt.Errorf("finding workspace: %w", err), ExitCode: 1}
	}
	tarCmd := exec.Command("tar", "--exclude", "target", "-C", wd, "-cf", "-", ".")
	load := exec.Command("docker", "run", "--rm", "-i", "--entrypoint", "sh",
		"-v", volume+":"+target, image, "-c", "mkdir -p "+target+" && tar -xf - -C "+target)
	reader, writer := io.Pipe()
	tarCmd.Stdout = writer
	load.Stdin = reader
	var stderr strings.Builder
	tarCmd.Stderr = &stderr
	load.Stderr = &stderr
	if err := tarCmd.Start(); err != nil {
		return DockerResult{Error: err, ExitCode: 1}
	}
	if err := load.Start(); err != nil {
		_ = writer.Close()
		_ = tarCmd.Wait()
		return DockerResult{Error: err, Stderr: stderr.String(), ExitCode: 1}
	}
	tarErr := make(chan error, 1)
	go func() {
		err := tarCmd.Wait()
		_ = writer.Close()
		tarErr <- err
	}()
	loadErr := load.Wait()
	if err := <-tarErr; err != nil {
		return DockerResult{Error: fmt.Errorf("archiving workspace: %w", err), Stderr: stderr.String(), ExitCode: 1}
	}
	if loadErr != nil {
		return DockerResult{Error: fmt.Errorf("loading workspace into volume: %w", loadErr), Stderr: stderr.String(), ExitCode: 1}
	}
	return DockerResult{}
}

func splitBind(spec string) (source, target string, ok bool) {
	parts := strings.Split(spec, ":")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func cacheVolumeName(source string) string {
	var b strings.Builder
	b.WriteString("gosonic-cache-")
	for _, r := range strings.ToLower(source) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.TrimRight(b.String(), "-.")
}

func startServices(stage StageExecution) (string, func(), error) {
	noop := func() {}
	if len(stage.Services) == 0 {
		return "", noop, nil
	}
	network := fmt.Sprintf("gosonic-%d", time.Now().UnixNano())
	if result := ExecDocker([]string{"docker", "network", "create", network}); result.Error != nil {
		return "", noop, fmt.Errorf("creating service network: %w", result.Error)
	}
	var names []string
	cleanup := func() {
		for _, name := range names {
			ExecDocker([]string{"docker", "rm", "-f", name})
		}
		ExecDocker([]string{"docker", "network", "rm", network})
	}
	for _, svc := range stage.Services {
		container := network + "-" + strings.TrimPrefix(cacheVolumeName(svc.Name), "gosonic-cache-")
		args := []string{"docker", "run", "-d", "--name", container, "--network", network, "--network-alias", svc.Name}
		for k, v := range svc.Environment {
			args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
		}
		for _, port := range svc.Ports {
			args = append(args, "-p", port)
		}
		args = append(args, svc.Image)
		if result := ExecDocker(args); result.Error != nil {
			cleanup()
			return "", noop, fmt.Errorf("starting service %s: %w", svc.Name, result.Error)
		}
		names = append(names, container)
	}
	return network, cleanup, nil
}
