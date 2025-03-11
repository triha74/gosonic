# gosonic

A modern, fully portable, Docker-based CI/CD task execution tool written in Go.

## Overview

gosonic provides a unified way to build, test, package and deploy applications using Docker containers. It uses a simple YAML configuration file to define stages and their execution environments. gosonic is not a CI/CD pipeline tool, it is a task execution tool, which means that its used within a CI/CD pipeline task to execute a set of commands. So while its similar to tools like Github Actions in its configuration, it is not ment as a replacement for a CI/CD pipeline tool but rather as a replacement for tools like `make` or `Makefile` to execute a set of commands.


## Features

- 🐳 Docker-based execution environments
- 📝 Simple YAML configuration
- 🔄 Multi-stage pipelines
- 🔍 Built-in audit logging
- 🌐 Support for matrix builds
- 📦 Volume mounting for caching and artifacts
- 🔒 Secure environment variable handling
- 🏃 Parallel execution support

## Installation

### From Source

Requirements:
- Go 1.21 or later
- Docker

```bash
# Clone the repository
git clone https://github.com/triha/gosonic.git
cd gosonic

# Build and install
./build.sh
```

The build script will:
1. Clean previous builds
2. Update dependencies
3. Vendor dependencies
4. Run tests
5. Build the binary
6. Install it to your $GOPATH/bin

### Using go install

```bash
go install github.com/triha74/go-sonic@latest
```

## Quick Start

1. Create a `.sonic.yml` file in your project root:

```yaml
version: "1"
project:
  name: "my-project"
  language: "go"
  root: "."
  audit:
    store: "file"        # "file" or "s3"
    path: ".logs"        # Directory for file store or S3 prefix
    s3bucket: ""         # S3 bucket name if using S3
stages:
  test:
    runner: "docker/library/golang:1.24.1-alpine"
    commands:
      - "go test ./..."
  build:
    runner: "docker/library/golang:1.24.1-alpine"
    commands:
      - "go build -o app"
```

2. Run a stage:

```bash
gosonic run test
```

## Configuration

### Project Structure

```yaml
version: "1"           # Configuration version
project:
  name: string        # Project name
  language: string    # Project language
  root: string       # Project root directory
  audit:
    store: string     # "file" or "s3"
    path: string      # Directory for file store or S3 prefix
    s3bucket: string  # S3 bucket name if using S3
stages:
  stage_name:        # Stage definition
    runner: string   # Docker image to use
    commands: []     # Commands to execute
    volumes: []      # Volume mounts
    environment: {}  # Environment variables
    requires: []     # List of stages that must complete successfully first
```

Example:
```yaml
version: "1"
project:
  name: "my-service"
  language: "go"
  root: "."
  audit:
    store: "file"
    path: ".logs"
stages:
  test:
    runner: "docker/library/golang:1.20-alpine3.17"
    commands: 
      - "go test ./..."
  build:
    runner: "docker/library/golang:1.20-alpine3.17"
    requires: ["test"]  # Build requires test to pass
    commands: 
      - "go build"
  deploy:
    requires: ["build", "test"]  # Deploy requires both
    commands: 
      - "./deploy.sh"
```

### Stages

Each stage can define:

- `runner`: Docker image to use (e.g., "docker/library/golang:1.20-alpine3.17")
- `commands`: List of commands to execute
- `volumes`: List of volume mounts
- `environment`: Map of environment variables
- `requires`: List of stages that must complete successfully before this stage can run
- `timeout`: Maximum execution time

Example with stage dependencies:

```yaml
stages:
  test:
    runner: "docker/library/golang:1.20-alpine3.17"
    commands:
      - "go test ./..."
  
  build:
    runner: "docker/library/golang:1.20-alpine3.17"
    requires: ["test"]  # Build only runs if tests pass
    commands:
      - "go build -o app"
  
  deploy:
    runner: "kubernetes"
    requires: ["build", "test"]  # Deploy requires both build and test to pass
    commands:
      - "kubectl apply -f k8s/"
```

When running a stage with dependencies:
- gosonic verifies if all required stages have completed successfully
- Required stages must be run before the dependent stage
- Dependencies are verified using the audit logs from previous runs

Example execution:
```bash
# This will fail because 'test' hasn't run yet
gosonic run build

# This will work - run test first, then build
gosonic run test  build

# This will fail because build hasn't run yet
gosonic run deploy

# This will work - run all stages in the correct order. Evalaution uses audit logs to determine if the stage has completed successfully.
gosonic run test 
gosonic build deploy
```

Note: gosonic does not automatically run required stages. You must explicitly run stages in the correct order.

### Volume Mounts

By default, go-sonic automatically mounts the current directory (`.`) to `/workspace` in the container. This can be overridden by explicitly defining a different workspace mount.

```yaml
volumes:
  - type: bind          # bind, cache, or tmp
    source: "./src"     # Host path
    target: "/app/src"  # Container path
    readonly: true      # Optional, mount as read-only
```

Example with minimal configuration:
```yaml
stages:
  test:
    runner: "docker/library/golang:1.20-alpine3.17"
    commands:
      - "go test ./..."    # Will run in /workspace
```

Example overriding the default:
```yaml
stages:
  test:
    runner: "docker/library/golang:1.20-alpine3.17"
    commands:
      - "go test ./..."
    volumes:
      - type: bind
        source: "."
        target: "/app"    # Use different workspace
```

### Runner Configuration

The `runner` field in a stage specifies which Docker image to use for execution. The runner can be configured in several ways:

Runner resolution follows these rules:

Default values:
- Default registry: `public.ecr.aws` (hardcoded, cannot be changed via flags)
- Default runner: `public.ecr.aws/docker/library/alpine:latest`

1. If a full image reference is provided (contains domain), it's used as-is:
   ```yaml
   runner: "docker.io/library/docker/library/golang:1.20-alpine3.17"
   ```

2. If only image name is provided, the default registry is prepended:
   ```yaml
   runner: "docker/library/golang:1.20-alpine3.17"
   # Resolves to: "public.ecr.aws/docker/library/golang:1.20-alpine3.17"
   ```

3. If no runner is specified, the default runner is used:
   ```yaml
   # Resolves to: "public.ecr.aws/docker/library/alpine:latest"
   ```

The runner image is used to create a container with:
- Current directory mounted at `/workspace` (unless overridden)
- Working directory set to `/workspace`
- Commands executed using `sh -c`
- Container removed after execution (`--rm`)
- Init process enabled (`--init`)

You can override these defaults using the `volumes` and other configuration options in the stage definition.

## Command Line Interface

```
gosonic [global options] command [command options] [arguments...]

COMMANDS:
   run      Run one or more stages in sequence
   help     Show help
   
GLOBAL OPTIONS:
   --sonic-file value, -f value    Path to sonic configuration file (default: ".sonic.yml")
                                   Environment: SONIC_CONFIG_FILE
   
   --var value, -v value           Execution variables in key=value format (can be specified multiple times)
                                   Environment: SONIC_VARS
   
   --audit-store value             Audit log storage type (file or s3)
                                   Environment: SONIC_AUDIT_STORE
   
   --audit-path value              Path for audit logs (directory for file store, prefix for S3)
                                   Environment: SONIC_AUDIT_PATH
   
   --audit-s3-bucket value         S3 bucket name for audit logs when using s3 store
                                   Environment: SONIC_AUDIT_S3_BUCKET
   
   --registry value                Default Docker registry to use when not specified in image reference
                                   Default: "public.ecr.aws"
                                   Environment: GOSONIC_DEFAULT_REGISTRY
   
   --help, -h                      Show help
```

### Environment Variables

All command line flags can also be set using environment variables:

- `SONIC_CONFIG_FILE`: Path to configuration file
- `SONIC_VARS`: Comma-separated list of key=value pairs
- `SONIC_AUDIT_STORE`: Audit log storage type
- `SONIC_AUDIT_PATH`: Path for audit logs
- `SONIC_AUDIT_S3_BUCKET`: S3 bucket for audit logs
- `GOSONIC_DEFAULT_REGISTRY`: Default Docker registry

Example using environment variables:
```bash
export SONIC_AUDIT_STORE=s3
export SONIC_AUDIT_S3_BUCKET=my-audit-logs
export SONIC_AUDIT_PATH=ci-logs/
gosonic run build
```

## Execution Variables

You can pass variables to be used during stage execution using the `--var` flag. These variables can be referenced in your configuration using `${variable}` syntax.

```bash
# Run deploy stage with specific region
gosonic run deploy --var region.name=us-east-1

# Multiple variables can be specified
gosonic run deploy --var region.name=us-east-1 --var env=prod
```

### Using Variables in Configuration

Variables can be used in:
- Environment variables
- Volume paths

Example:
```yaml
stages:
  deploy:
    runner: "kubernetes"
    commands:
      - "kubectl apply -f k8s/"
    volumes:
      - type: bind
        source: "${HOME}/.kube/${region.name}/config"
        target: "/root/.kube/config"
        readonly: true
    environment:
      KUBECONFIG: "/root/.kube/config"
      REGION: "${region.name}"
      ENV: "${env}"
```

## Audit Logging

go-sonic automatically audit logs all stage executions. Each log includes:
- Project and stage information
- Git revision
- Command executed
- Start time and duration
- Execution status and any errors

### Configuration

Audit logging can be configured in two ways:

1. In the configuration file:
```yaml
project:
  name: "my-service"
audit:
  store: "file"        # "file" or "s3"
  path: ".logs"        # Directory for file store or S3 prefix
  s3bucket: ""         # S3 bucket name if using S3 store
```

2. Using command line flags or environment variables:
```bash
# Using flags
gosonic run build --audit-store=file --audit-path=.logs

# Using environment variables
export SONIC_AUDIT_STORE=s3
export SONIC_AUDIT_S3_BUCKET=my-audit-logs
export SONIC_AUDIT_PATH=ci-logs/
```

### Storage Options

#### File Store (Default)
- Type: `file`
- Stores logs as JSON files in a local directory
- Default directory: `.logs`
- Configure path using:
  - Config: `audit.path`
  - Flag: `--audit-path`
  - Environment: `SONIC_AUDIT_PATH`

Example file store configuration:
```yaml
audit:
  store: "file"
  path: ".logs"
```

#### S3 Store
- Type: `s3`
- Stores logs as JSON files in an S3 bucket
- Requires:
  - S3 bucket name
  - Optional prefix for organizing logs
- Configure using:
  - Config: `audit.s3bucket` and `audit.path`
  - Flags: `--audit-s3-bucket` and `--audit-path`
  - Environment: `SONIC_AUDIT_S3_BUCKET` and `SONIC_AUDIT_PATH`

Example S3 configuration:
```yaml
audit:
  store: "s3"
  s3bucket: "my-audit-logs"
  path: "ci-logs/"
```

### Configuration Priority

The audit store configuration is resolved in this order:
1. Command line flags
2. Environment variables
3. Configuration file
4. Defaults (file store in `.logs` directory)

## Development

Requirements:
- Go 1.21 or later
- Docker

### Building from Source

```bash
# Clone the repository
git clone https://github.com/yourusername/go-sonic.git
cd go-sonic

# Install dependencies
go mod tidy
go mod vendor

# Run tests
go test ./...

# Build the binary
go build

# Install locally
go install
```

### Running Tests

```bash
# Run all tests
go test ./...

# Run tests with coverage
go test -cover ./...

# Run tests verbosely
go test -v ./...
```

## License

MIT License - see LICENSE file for details

### Project Configuration

The `project` section in the configuration defines the basic properties of your project:

```yaml
project:
  name: string        # Required. Name of your project
  language: string    # Optional. Programming language (e.g., "go", "python")
  root: string       # Optional. Project root directory, defaults to "."
```

Example configurations:

```yaml
# Minimal configuration
project:
  name: "my-service"

# Full configuration
project:
  name: "hello-world"
  language: "go"
  root: "."

# Custom root directory
project:
  name: "web-app"
  language: "javascript"
  root: "./src"
```

The project properties are used for:
- `name`: Used in audit logs and error messages
- `language`: Currently informational only
- `root`: Base directory for relative paths in volume mounts
