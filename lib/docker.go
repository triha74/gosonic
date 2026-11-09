package lib

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		err = fmt.Errorf("timed out: %w", ctx.Err())
	case errors.Is(ctx.Err(), context.Canceled):
		err = ctx.Err()
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

var builtinRegistries = []string{"docker.io", "public.ecr.aws", "ghcr.io", "quay.io", "localhost"}

var warnedDigests sync.Map

// RegistryAllowed reports whether host may be used as an image registry.
// extra lists workflow-configured registries. A port is ignored when the hostname is allowed.
func RegistryAllowed(host string, extra []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	name := host
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		name = parsed
	}
	for _, allowed := range append(append([]string{}, builtinRegistries...), extra...) {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if allowed == "" {
			continue
		}
		if host == allowed || name == allowed {
			return true
		}
	}
	return false
}

// ValidateImage rejects registries outside the allowlist.
// Images without a digest warn, or fail when requireDigest is set.
func ValidateImage(image string, extra []string, requireDigest bool) error {
	ref := ParseImageRef(image)
	if ref.Domain == "" || !RegistryAllowed(ref.Domain, extra) {
		return fmt.Errorf("registry %q is not allowed for image %s", ref.Domain, image)
	}
	if ref.Digest != "" {
		return nil
	}
	if requireDigest {
		return fmt.Errorf("image %s has no @sha256 digest", image)
	}
	if _, loaded := warnedDigests.LoadOrStore(image, true); !loaded {
		fmt.Fprintf(os.Stderr, "warning: image %s has no @sha256 digest\n", image)
	}
	return nil
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
	Name              string
	Runner            string
	Commands          []string
	Shell             string
	Environment       map[string]string
	Volumes           []Volume
	Verbose           bool
	PrefixOutput      bool
	OutputLock        *sync.Mutex
	WorkingDirectory  string
	Timeout           time.Duration
	DockerSocket      bool
	AllowDockerSocket bool
	Secrets           []string
	User              string
	PidsLimit         int
	Services          []ServiceSpec
	Context           context.Context
}

// ExecuteStage runs a stage in a docker container and handles audit logging
func ExecuteStage(stage StageExecution, auditStore AuditStore, projectName string) error {
	if stage.DockerSocket && !stage.AllowDockerSocket {
		return fmt.Errorf("docker socket is not allowed; pass --allow-docker-socket or set GOSONIC_ALLOW_DOCKER_SOCKET=1")
	}
	startTime := time.Now()

	// Get git revision
	gitRev, err := GetGitRevision()
	if err != nil {
		gitRev = "unknown" // Don't fail if we can't get git revision
	}

	envFile, err := writeEnvFile(stage.Environment, stage.Secrets)
	if err != nil {
		return err
	}
	if envFile != "" {
		defer os.Remove(envFile)
	}

	network, cleanup, err := startServices(stage)
	if err != nil {
		return err
	}
	defer cleanup()

	dockerArgs, err := buildDockerArgs(stage, network, envFile)
	if err != nil {
		return err
	}

	// A single simple command is the container entrypoint. Multiple commands,
	// or a command that needs a shell, run under <shell> -c. The shell expands
	// container environment, including matrix values and --var names.
	if len(stage.Commands) == 1 && !strings.Contains(stage.Commands[0], "\n") {
		dockerArgs = append(dockerArgs, splitCommandArgs(stage.Commands[0])...)
	} else if script := joinShellCommands(stage.Commands); script != "" {
		dockerArgs = append(dockerArgs, containerShell(stage.Shell), "-c", script)
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
		if stage.Verbose {
			stage.printf("Docker cannot mount the workspace from this host; copying it into the container.\n")
		}
		result = runWithCopiedBinds(stage, dockerArgs)
	}

	// Verbose streams live. Quiet keeps the buffer and prints it only on failure.
	if stage.Verbose && !result.Streamed {
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
	if result.Error != nil && !stage.Verbose {
		return &StageOutputError{Err: result.Error, Stdout: result.Stdout, Stderr: result.Stderr}
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

// joinShellCommands chains stage commands with && in one shell.
// YAML block scalars keep a trailing newline. Joining that raw string with
// " && " puts the operator on the next line, which bash treats as a new
// statement ("syntax error near unexpected token `&&'").
func joinShellCommands(commands []string) string {
	parts := make([]string, 0, len(commands))
	for _, cmd := range commands {
		cmd = strings.TrimRight(cmd, "\r\n")
		if strings.TrimSpace(cmd) == "" {
			continue
		}
		parts = append(parts, cmd)
	}
	return strings.Join(parts, " && ")
}

// containerShell is the program used for multiline or multi-command stages.
// GitHub Actions uses bash; Alpine images typically only have sh. Empty means sh.
func containerShell(name string) string {
	if strings.TrimSpace(name) == "" {
		return "sh"
	}
	return name
}

func buildDockerArgs(stage StageExecution, network, envFile string) ([]string, error) {
	if stage.DockerSocket && !stage.AllowDockerSocket {
		return nil, fmt.Errorf("docker socket is not allowed; pass --allow-docker-socket or set GOSONIC_ALLOW_DOCKER_SOCKET=1")
	}
	workdir := stage.WorkingDirectory
	if workdir == "" {
		workdir = "/workspace"
	}
	user, groups := containerIdentity(stage)
	dockerArgs := []string{
		"docker", "run",
		"--rm",
		"--init",
		"--workdir", workdir,
		"--user", user,
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--pids-limit", pidsLimit(stage),
	}
	for _, gid := range groups {
		dockerArgs = append(dockerArgs, "--group-add", gid)
	}
	if network != "" {
		dockerArgs = append(dockerArgs, "--network", network)
	}
	if envFile != "" {
		dockerArgs = append(dockerArgs, "--env-file", envFile)
	}

	volumes := append([]Volume{}, stage.Volumes...)
	if stage.DockerSocket {
		volumes = append(volumes, Volume{
			Type:   "bind",
			Source: "/var/run/docker.sock",
			Target: "/var/run/docker.sock",
		})
	}
	var readonly []Volume
	for _, vol := range volumes {
		if (vol.Type == "" || vol.Type == "bind") && isWorkspaceBind(vol.Source) {
			readonly = append(readonly, readonlyMetadataMounts(vol.Source, vol.Target)...)
		}
	}
	volumes = append(volumes, readonly...)

	_, keyPath, err := loadAuditKey()
	if err != nil {
		return nil, err
	}
	for _, vol := range volumes {
		if (vol.Type == "" || vol.Type == "bind") && mountsAuditKey(vol.Source, keyPath) {
			return nil, fmt.Errorf("refusing to mount %s: it includes the audit signing key", vol.Source)
		}
		arg, err := vol.dockerVolumeArg()
		if err != nil {
			return nil, err
		}
		dockerArgs = append(dockerArgs, "-v", arg)
	}

	dockerArgs = append(dockerArgs, stage.Runner)
	return dockerArgs, nil
}

func pidsLimit(stage StageExecution) string {
	if stage.PidsLimit > 0 {
		return strconv.Itoa(stage.PidsLimit)
	}
	return "4096"
}

func containerIdentity(stage StageExecution) (string, []string) {
	user := stage.User
	if user == "" {
		user = fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	}
	var groups []string
	if stage.DockerSocket && stage.AllowDockerSocket {
		if gid, err := dockerSocketGroup(); err == nil && gid != "" {
			groups = append(groups, gid)
		}
	}
	return user, groups
}

func dockerSocketGroup() (string, error) {
	info, err := os.Stat("/var/run/docker.sock")
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("docker socket has no owner")
	}
	return strconv.FormatUint(uint64(stat.Gid), 10), nil
}

func isWorkspaceBind(source string) bool {
	if source == "." || source == "./" {
		return true
	}
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return false
	}
	return filepath.Clean(abs) == filepath.Clean(cwd)
}

func readonlyMetadataMounts(source, target string) []Volume {
	abs, err := filepath.Abs(source)
	if err != nil {
		return nil
	}
	var mounts []Volume
	for _, name := range []string{".git", ".sonic"} {
		host := filepath.Join(abs, name)
		if _, err := os.Lstat(host); err != nil {
			continue
		}
		mounts = append(mounts, Volume{
			Type:     "bind",
			Source:   host,
			Target:   strings.TrimRight(target, "/") + "/" + name,
			Readonly: true,
		})
	}
	return mounts
}

// mountsAuditKey reports whether a bind of source would expose the signing key.
// A mount of the key file, its directory, or any parent directory counts.
func mountsAuditKey(source, keyPath string) bool {
	if source == "" || keyPath == "" {
		return false
	}
	src, err := filepath.Abs(source)
	if err != nil {
		return false
	}
	key, err := filepath.Abs(keyPath)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(src), filepath.Clean(key))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func writeEnvFile(env map[string]string, secrets []string) (string, error) {
	merged := make(map[string]string, len(env)+len(secrets))
	for key, value := range env {
		if ProtectedEnv(key) {
			continue
		}
		merged[key] = value
	}
	for _, name := range secrets {
		if name == "" {
			return "", fmt.Errorf("secret name is empty")
		}
		if ProtectedEnv(name) {
			continue
		}
		value, ok := os.LookupEnv(name)
		if !ok {
			return "", fmt.Errorf("secret %s is not set in the environment", name)
		}
		merged[name] = value
	}
	if len(merged) == 0 {
		return "", nil
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var body strings.Builder
	for _, key := range keys {
		body.WriteString(envFileLine(key, merged[key]))
		body.WriteByte('\n')
	}
	file, err := os.CreateTemp("", "gosonic-env-*")
	if err != nil {
		return "", fmt.Errorf("creating env file: %w", err)
	}
	path := file.Name()
	if err := file.Chmod(0600); err != nil {
		file.Close()
		os.Remove(path)
		return "", err
	}
	if _, err := file.WriteString(body.String()); err != nil {
		file.Close()
		os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

func envFileLine(key, value string) string {
	if value == "" || strings.ContainsAny(value, "\r\n\"'\\ #") || strings.TrimSpace(value) != value {
		escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`).Replace(value)
		return key + `="` + escaped + `"`
	}
	return key + "=" + value
}

func publishedPort(spec string) string {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.HasPrefix(spec, "[") {
		return spec
	}
	parts := strings.Split(spec, ":")
	if len(parts) >= 3 || (len(parts) == 2 && strings.Contains(parts[0], ".")) {
		return spec
	}
	return "127.0.0.1:" + spec
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
	ctx := stage.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return DockerResult{Error: err, ExitCode: 1}
	}
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
	return runDockerContainer(ctx, args, stdout, stderr)
}

// runDockerContainer starts a docker run specification through create, start,
// and logs --follow. Docker Desktop can drop output attached directly to
// docker run, while the logs stream retains it. Creation failures, including
// a denied workspace mount, stay captured so the caller can recover without
// already having printed them.
func runDockerContainer(ctx context.Context, args []string, stdout, stderr io.Writer) DockerResult {
	if len(args) < 2 || args[0] != "docker" || args[1] != "run" {
		return runDockerStreaming(ctx, args, stdout, stderr)
	}

	created := runDocker(ctx, dockerCreateArgs(args))
	if created.Error != nil {
		return created
	}
	id := strings.TrimSpace(created.Stdout)
	if id == "" {
		return DockerResult{
			Error:    fmt.Errorf("docker create returned no container id"),
			Stderr:   created.Stderr,
			ExitCode: 1,
		}
	}
	defer func() {
		_ = runDocker(context.Background(), []string{"docker", "rm", "-f", id})
	}()

	if started := runDocker(ctx, []string{"docker", "start", id}); started.Error != nil {
		return containerResult(ctx, started, false)
	}
	logs := runDockerStreaming(ctx, []string{"docker", "logs", "--follow", id}, stdout, stderr)
	if ctx.Err() != nil {
		return containerResult(ctx, logs, logs.Streamed)
	}

	inspected := runDocker(context.Background(), []string{"docker", "inspect", "-f", "{{.State.ExitCode}}", id})
	if inspected.Error != nil {
		if logs.Error != nil {
			return logs
		}
		inspected.Stdout = logs.Stdout
		inspected.Stderr = logs.Stderr
		inspected.Streamed = logs.Streamed
		return inspected
	}
	code, err := strconv.Atoi(strings.TrimSpace(inspected.Stdout))
	if err != nil {
		return DockerResult{
			Stdout:   logs.Stdout,
			Stderr:   logs.Stderr,
			Error:    fmt.Errorf("reading container exit code: %w", err),
			ExitCode: 1,
			Streamed: logs.Streamed,
		}
	}
	result := logs
	result.ExitCode = code
	result.Error = nil
	if code != 0 {
		result.Error = fmt.Errorf("exit status %d", code)
	}
	return result
}

func dockerCreateArgs(runArgs []string) []string {
	createArgs := make([]string, 0, len(runArgs))
	createArgs = append(createArgs, "docker", "create")
	for _, arg := range runArgs[2:] {
		if arg == "--rm" {
			continue
		}
		createArgs = append(createArgs, arg)
	}
	return createArgs
}

func containerResult(ctx context.Context, result DockerResult, streamed bool) DockerResult {
	result.Streamed = streamed
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.Error = fmt.Errorf("timed out: %w", ctx.Err())
		if result.ExitCode == 0 {
			result.ExitCode = 124
		}
	case errors.Is(ctx.Err(), context.Canceled):
		result.Error = ctx.Err()
		if result.ExitCode == 0 {
			result.ExitCode = 1
		}
	}
	return result
}

func (stage StageExecution) consoleWriters() (io.Writer, io.Writer) {
	if !stage.Verbose {
		return nil, nil
	}
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
	var envFiles []string
	cleanup := func() {
		for _, name := range names {
			ExecDocker([]string{"docker", "rm", "-f", name})
		}
		ExecDocker([]string{"docker", "network", "rm", network})
		for _, path := range envFiles {
			os.Remove(path)
		}
	}
	for _, svc := range stage.Services {
		container := network + "-" + strings.TrimPrefix(cacheVolumeName(svc.Name), "gosonic-cache-")
		envFile, err := writeEnvFile(svc.Environment, nil)
		if err != nil {
			cleanup()
			return "", noop, err
		}
		if envFile != "" {
			envFiles = append(envFiles, envFile)
		}
		args := []string{"docker", "run", "-d", "--name", container, "--network", network, "--network-alias", svc.Name}
		if envFile != "" {
			args = append(args, "--env-file", envFile)
		}
		for _, port := range svc.Ports {
			args = append(args, "-p", publishedPort(port))
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
