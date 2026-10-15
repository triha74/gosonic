package lib

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
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
	// Streamed is set when stdout and stderr were already written to the console
	// as the process produced them.
	Streamed bool
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
	return runDockerStreaming(ctx, args, nil, nil)
}

// runDockerStreaming runs args[0] and copies stdout and stderr into the returned
// result. When stdout or stderr is non-nil, each chunk is also written there as
// it arrives. A nil writer means capture only.
func runDockerStreaming(ctx context.Context, args []string, stdout, stderr io.Writer) DockerResult {
	if len(args) == 0 {
		return DockerResult{Error: fmt.Errorf("docker command is empty"), ExitCode: 1}
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var outBuf, errBuf strings.Builder
	stream := stdout != nil || stderr != nil
	if stream {
		if stdout == nil {
			stdout = io.Discard
		}
		if stderr == nil {
			stderr = io.Discard
		}
		cmd.Stdout = io.MultiWriter(&outBuf, stdout)
		cmd.Stderr = io.MultiWriter(&errBuf, stderr)
	} else {
		cmd.Stdout = &outBuf
		cmd.Stderr = &errBuf
	}

	err := cmd.Run()
	flushWriter(stdout)
	flushWriter(stderr)
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
		Stdout:   outBuf.String(),
		Stderr:   errBuf.String(),
		Error:    err,
		ExitCode: exitCode,
		Streamed: stream,
	}
}

func flushWriter(w io.Writer) {
	if f, ok := w.(interface{ Flush() }); ok {
		f.Flush()
	}
}

// dockerRunnerReplaced reports whether a test replaced ExecDocker. Production
// keeps execDockerImpl, which is what streaming uses to tell those paths apart.
func dockerRunnerReplaced() bool {
	return reflect.ValueOf(ExecDocker).Pointer() != reflect.ValueOf(execDockerImpl).Pointer()
}

func dockerCtxReplaced() bool {
	return reflect.ValueOf(ExecDockerCtx).Pointer() != reflect.ValueOf(runDocker).Pointer()
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

	// Quote arguments so the printed command is what the host would run.
	// exec receives each argument separately; joining with spaces drops the
	// quotes around the sh -c script.
	fullCommand := formatCommand(dockerArgs)

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

	result := stage.invokeDocker(dockerArgs)
	if result.Error != nil && hostMountDenied(result) {
		stage.printf("Docker cannot mount the workspace from this host; copying it into the container.\n")
		result = runWithCopiedBinds(stage, dockerArgs)
	}

	// Mocked docker returns a finished result. A real container already streamed.
	if !result.Streamed {
		stage.writeOutput(result.Stdout, result.Stderr)
	}

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
	return stage.invokeDocker(swapRelativeBinds(dockerArgs, volume))
}

// invokeDocker runs the stage container. Tests that replace ExecDocker or
// ExecDockerCtx keep that path. The production path streams stdout and stderr
// and still records them on the result.
func (stage StageExecution) invokeDocker(args []string) DockerResult {
	ctx := context.Background()
	if stage.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, stage.Timeout)
		defer cancel()
		if dockerCtxReplaced() {
			return ExecDockerCtx(ctx, args)
		}
	}
	if dockerRunnerReplaced() {
		return ExecDocker(args)
	}
	stdout, stderr := stage.consoleWriters()
	return runDockerStreaming(ctx, args, stdout, stderr)
}

func (stage StageExecution) consoleWriters() (io.Writer, io.Writer) {
	if !stage.PrefixOutput {
		return os.Stdout, os.Stderr
	}
	return &linePrefixWriter{w: os.Stdout, prefix: stage.Name, mu: stage.OutputLock},
		&linePrefixWriter{w: os.Stderr, prefix: stage.Name, mu: stage.OutputLock}
}

// linePrefixWriter prefixes each complete line. Partial chunks stay buffered so
// parallel stages do not interleave mid-line. Flush emits a trailing partial line.
type linePrefixWriter struct {
	mu     *sync.Mutex
	w      io.Writer
	prefix string
	rest   []byte
}

func (p *linePrefixWriter) Write(b []byte) (int, error) {
	if p.mu != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
	}
	p.rest = append(p.rest, b...)
	for {
		i := bytes.IndexByte(p.rest, '\n')
		if i < 0 {
			return len(b), nil
		}
		line := string(p.rest[:i])
		p.rest = append([]byte(nil), p.rest[i+1:]...)
		fmt.Fprintf(p.w, "%s | %s\n", p.prefix, line)
	}
}

func (p *linePrefixWriter) Flush() {
	if p.mu != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
	}
	if len(p.rest) == 0 {
		return
	}
	fmt.Fprintf(p.w, "%s | %s\n", p.prefix, p.rest)
	p.rest = nil
}

// formatCommand renders argv the way a shell would have to quote it.
// The process is still started with the raw arguments.
func formatCommand(args []string) string {
	parts := make([]string, len(args))
	for i, arg := range args {
		parts[i] = quoteArg(arg)
	}
	return strings.Join(parts, " ")
}

func quoteArg(arg string) string {
	if arg == "" {
		return "''"
	}
	if isBareArg(arg) {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'"'"'`) + "'"
}

func isBareArg(arg string) bool {
	for _, r := range arg {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("@%+=:,./_-^", r):
		default:
			return false
		}
	}
	return true
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
