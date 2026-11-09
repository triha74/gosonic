# GoSonic - Docker-based CI/CD Task Runner

# gosonic

A modern, fully portable, Docker-based CI/CD task execution tool written in Go.

## Overview

gosonic is a Docker-based task execution tool that provides a unified approach to building, testing, packaging, and deploying applications. While it shares configuration similarities with CI/CD platforms like GitHub Actions, gosonic is specifically designed as a modern replacement for traditional build tools like `make`.

Key distinctions:
- **Task Executor, Not Pipeline Tool**: gosonic executes tasks within CI/CD pipelines rather than replacing the pipeline itself
- **Docker-First Design**: All tasks run in isolated Docker containers, ensuring consistent execution environments
- **Platform Agnostic**: Works identically across different CI/CD platforms (GitHub Actions, GitLab CI, Jenkins, etc.)
- **Simple Configuration**: Each workflow is a YAML file under `.sonic/` that defines stages and their environments

Think of gosonic as a more powerful, containerized alternative to Makefiles, bringing modern Docker-based isolation and configuration to task execution while maintaining compatibility with any CI/CD platform.

## Features

- 🐳 Docker-based execution environments
- 📝 Simple YAML configuration
- 🔄 Multi-stage pipelines
- 🔍 Built-in audit logging
- 🌐 Support for matrix builds
- 📦 Volume mounting for caching and artifacts
- 🔒 Secure environment variable handling
- 🏃 Parallel execution support

## Table of Contents

1. [Introduction](#introduction)
2. [Installation](#installation)
3. [Configuration](#configuration)
   - [Project Structure](#project-structure)
   - [Stages](#stages)
   - [Workflows](#workflows)
   - [Suites](#suites)
   - [Runner Configuration](#runner-configuration)
   - [Volume Mounts](#volume-mounts)
4. [Command Line Interface](#command-line-interface)
   - [Environment Variables](#environment-variables)
5. [Execution Variables](#execution-variables)
   - [Using Variables in Configuration](#using-variables-in-configuration)
6. [Audit Logging](#audit-logging)
   - [Configuration](#configuration-1)
   - [Signatures](#signatures)
   - [AWS Credentials for S3 Audit Logging](#aws-credentials-for-s3-audit-logging)
7. [CI/CD Integration](#cicd-integration)
   - [GitHub Actions](#github-actions)
   - [GitLab CI](#gitlab-ci)
   - [Azure DevOps](#azure-devops)
   - [Benefits of gosonic in CI/CD](#benefits-of-gosonic-in-cicd)
8. [Security](#security)
   - [Secrets](#secrets)
   - [Signed audit records](#signed-audit-records)
   - [Container isolation](#container-isolation)
   - [Images](#images)
   - [Artifact paths](#artifact-paths)
9. [Development](#development)
   - [Building from Source](#building-from-source)
   - [Running Tests](#running-tests)
10. [License](#license)

## Installation

### From Source

Requirements:
- Go 1.24 or later
- Docker

```bash
# Clone the repository
git clone https://github.com/triha/gosonic.git
cd gosonic

# Build and install
make
```

`make` will:
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

1. Create `.sonic/ci.yml`:

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
gosonic ci run test
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
runners:             # Optional alias to Docker image
  - alpine: string
stages:
  stage_name:        # Stage definition
    runner: string   # Alias or Docker image to use
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

- `runner`: Alias defined in `runners`, or a Docker image (e.g., "docker/library/golang:1.20-alpine3.17")
- `commands`: List of commands to execute. gosonic does not substitute `${var}` inside commands
- `shell`: Program used for a multi-command stage. Default `sh`
- `volumes`: List of volume mounts
- `environment`: Map of environment variables. Values are passed in an env file, not on the `docker` command line
- `secrets`: Names of variables to read from the gosonic process environment. Values are not taken from YAML
- `requires`: List of stages that must have a signed successful audit record before this stage can run
- `timeout`: Maximum execution time
- `user`: Container user, passed as `--user`. Default is the host uid and gid
- `pids_limit`: Process limit inside the container. Default `4096`

Define an image once under `runners` and use the alias in each stage. A runner that does not match an alias is still used as an image reference.

```yaml
runners:
  - alpine: "docker/library/golang:1.20-alpine3.17"

stages:
  test:
    runner: alpine
```

A mapping works the same way:

```yaml
runners:
  alpine: "docker/library/golang:1.20-alpine3.17"
```

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
- gosonic checks that every required stage has a signed audit record with status `success` or `skipped`
- A missing signature, or a record signed with a different key, does not count
- Required stages must be run before the dependent stage
- The same signing key has to be available to the later run. Set `GOSONIC_AUDIT_HMAC_KEY` when runs do not share a home directory

Example execution:
```bash
# This will fail because 'test' hasn't run yet
gosonic ci run build

# This will work - run test first, then build
gosonic ci run test build

# This will fail because build hasn't run yet
gosonic ci run deploy

# This will work - run all stages in the correct order. Evaluation uses audit logs to determine if the stage has completed successfully.
gosonic ci run test
gosonic ci run build deploy
```

Note: gosonic does not automatically run required stages. You must name every stage you want to run. With the default `--jobs 1`, list them in dependency order.

### Workflows

A workflow is one file, `.sonic/<name>.yml`. The name is the word before `run`:

```bash
gosonic ci run --jobs 4 full-test
gosonic ci run lint corpus
gosonic --verbose ci run lint
```

`gosonic run full-test` uses the default file, in this order:

1. `.sonic/sonic.yml`
2. `.sonic.yml`
3. the only `*.yml` in `.sonic/`

Several workflow files and no `sonic.yml` require the name. `--sonic-file path.yml` selects that path and does not treat a leading word as a workflow. A workflow name is a single segment matching `[A-Za-z0-9_-]+`. `run`, `help`, and `verify-aws-creds` are commands, not workflow names.

`gosonic ci lint` does not run the `lint` stage. The workflow name is only recognized when the next word is `run`.

### Suites

A suite is a named list of stages. `gosonic run` expands that name into those stages, in the listed order. A suite does not run `requires` stages that it does not list.

```yaml
suites:
  smoke-test:
    - lint
    - product-truth
  full-test:
    - lint
    - product-truth
    - test
```

```bash
gosonic ci run smoke-test
gosonic ci run --jobs 4 full-test
gosonic ci run lint smoke-test
```

A stage named by more than one argument is kept at its first position. With `--jobs` greater than 1, the expanded stages use the same dependency scheduler as a list of stage names.

### Parallel stages

`--jobs` sets how many stages run at once. The default is 1, which runs the named stages in command-line order and checks `requires` against audit logs from earlier runs.

```bash
gosonic ci run --jobs 4 lint test build
```

When `--jobs` is greater than 1, gosonic schedules the stages you named:

- A stage starts only after every `requires` entry from this same command has succeeded in this run.
- A requirement that you did not name must already have a signed `success` or `skipped` audit record.
- A failure does not start anything further. Stages that have already started are allowed to finish.
- Output lines are prefixed with the stage name.

Parallel stages still share the default `.` bind mount at `/workspace`. Do not run stages together when they write the same paths.

### Volume Mounts

By default, gosonic mounts the current directory (`.`) at `/workspace`. When that bind is the workspace, `.git` and `.sonic` are mounted read-only on top of it, including when `.git` is a file in a worktree. A different workspace bind replaces the default. If the daemon rejects the bind because the path is not shared from the host, gosonic copies the working directory (except `target/`) into a temporary volume and mounts that instead.

A bind whose source is the audit signing key, the directory that holds it, or any parent of that directory is refused. Mounting `$HOME` fails for that reason when the key lives under the home directory. Mount `~/.aws` or another specific directory instead.

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

`type: cache` mounts a named Docker volume. The volume name is derived from `source`, so the same source name reuses the cache across stages and runs. `type: tmp` mounts an anonymous volume at `target`.

```yaml
volumes:
  - type: cache
    source: cargo-registry
    target: /root/.cargo/registry
  - type: tmp
    target: /tmp/work
```

### Actions, hooks, and stage controls

`actions` defines reusable bodies. A stage runs an action from `before`, `steps`, or `after`.

- An action with `runner` is its own container. Set `docker_socket: true` when that container should talk to the host Docker daemon, for example to `docker build` an image that later stages run. The run also needs `--allow-docker-socket` or `GOSONIC_ALLOW_DOCKER_SOCKET=1`.
- An action without `runner` is a command fragment. Its commands run inside the calling stage's container.
- `before` must succeed before the stage body starts. `after` defaults to `when: always`, so it also runs when the stage fails. The stage failure is still returned.
- `once: true` runs that action at most once per `gosonic run`. Every stage that lists it waits for that single run, including under `--jobs`.
- Each action execution is audited as `action:<name>`.

```yaml
actions:
  build-image:
    runner: docker-cli
    docker_socket: true
    once: true
    commands:
      - docker build -t localhost/app:dev .
  setup:
    commands:
      - rustup show

stages:
  test:
    runner: localhost/app:dev
    before:
      - build-image
      - setup
    commands:
      - cargo test
    after:
      - uses: setup
        when: failure
```

Other stage fields:

- `working_directory`: container workdir. Default `/workspace`.
- `timeout`: Go duration. The container is killed when it expires.
- `artifacts`: after a successful stage, copy these workspace-relative paths to `.sonic-artifacts/<stage>/`. A missing path fails the stage. A path that leaves the workspace, including through a symlink, is rejected.
- `coverage`: when `enabled` is true, read `file` from inside the workspace. `format: percent` reads a number; `format: go` reads a Go cover profile. The stage fails when the percentage is below `threshold`.
- `when`: skip the stage unless the expression matches. Supported forms are `env:NAME`, `env:NAME=value`, and a leading `!`. A skipped stage is audited as `skipped` and satisfies `requires`. The same expressions gate `after` actions, along with `always`, `success`, and `failure`.
- `docker_socket`: bind-mount `/var/run/docker.sock` for this stage. Ignored unless the run passes `--allow-docker-socket` or `GOSONIC_ALLOW_DOCKER_SOCKET` is `1` or `true`. The socket's group is added so a non-root user can use it. A volume entry that mounts the socket path directly is not covered by this flag.
- `secrets`: environment variable names. gosonic reads each value from its own environment and drops `GOSONIC_AUDIT_HMAC_KEY` and `GOSONIC_AUDIT_HMAC_KEY_FILE` if they appear here.
- `user`: replaces the default `--user <host-uid>:<host-gid>`. Use `root` only when the image has to.
- `pids_limit`: replaces the default pid limit of 4096.
- `services`: start named containers on a private network before the stage and remove them afterward. Each service has `image`, `environment`, and `ports`. The stage reaches a service by its name. A port such as `5432:5432` is published on `127.0.0.1`. A spec that already names an address, such as `0.0.0.0:5432:5432`, is left as written.
- `matrix`: list of dimension names declared under the top-level `matrix` map. Gosonic runs the Cartesian product. Each cell is a stage instance, and each dimension value is exported as an environment variable of that name. `--matrix key=a,b` overrides a dimension for that invocation.

A locally built image needs a registry host in its name. `localhost/app:dev` is used as written. A name with no registry host is prefixed with the default registry, `public.ecr.aws` unless `--registry` says otherwise. The host has to be on the registry allowlist. See [Images](#images).

### Runner Configuration

The `runner` field in a stage specifies which Docker image to use for execution. The runner can be configured in several ways:

Runner resolution follows these rules:

Default values:
- Default registry: `public.ecr.aws`, overridable with `--registry` or `GOSONIC_DEFAULT_REGISTRY` when that host is allowed
- Default runner, when a stage sets no runner: `<default-registry>/docker/library/alpine:latest`

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
- Current directory mounted at `/workspace` (unless overridden), with `.git` and `.sonic` read-only when that bind is the workspace
- Working directory set to `/workspace`
- A single simple command as the container entrypoint. Several commands, or a command that needs a shell, run under `sh -c` unless `shell` is set
- Environment and secrets passed with `--env-file`, then the file is removed
- Container user set to the host uid and gid, unless `user` is set
- `--cap-drop ALL`, `--security-opt no-new-privileges`, and `--pids-limit 4096`
- Container removed after execution (`--rm`)
- Init process enabled (`--init`)

An image reference without `@sha256:` prints a warning. `--require-image-digest` turns that warning into an error. Allowed registry hosts are `docker.io`, `public.ecr.aws`, `ghcr.io`, `quay.io`, and `localhost` (a port on an allowed host is fine). Add more with `allowed_registries` in the workflow. A host outside that set, including a default registry set with `--registry`, fails the stage before `docker run`.

## Command Line Interface

```
gosonic [workflow] run [stages|suites]
gosonic ci run --jobs 4 full-test

COMMANDS:
   run      Run stages or suites. Example: gosonic ci run --jobs 4 full-test
   help     Show help
   
GLOBAL OPTIONS:
   --sonic-file value, -f value    Path to a workflow file. Overrides the workflow name before run
                                   (default: ".sonic/sonic.yml")
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

   --allow-docker-socket           Allow stages and actions that set docker_socket
                                   Environment: GOSONIC_ALLOW_DOCKER_SOCKET

   --require-image-digest          Fail when an image has no @sha256 digest
   
   --verbose                       Enable verbose output
                                   Environment: GOSONIC_VERBOSE
   
   --help, -h                      Show help
```

### Environment Variables

All command line flags can also be set using environment variables:

- `SONIC_CONFIG_FILE`: Path to configuration file
- `SONIC_VARS`: Comma-separated list of key=value pairs
- `SONIC_AUDIT_STORE`: Audit log storage type
- `SONIC_AUDIT_PATH`: Path for audit logs
- `SONIC_AUDIT_S3_BUCKET`: S3 bucket for audit logs
- `GOSONIC_DEFAULT_REGISTRY`: Default Docker registry. The host must be on the allowlist
- `GOSONIC_ALLOW_DOCKER_SOCKET`: Set to `1` or `true` to allow `docker_socket`
- `GOSONIC_VERBOSE`: Enable verbose output
- `GOSONIC_AUDIT_HMAC_KEY`: Base64 signing key, at least 32 bytes after decoding. Takes precedence over the key file
- `GOSONIC_AUDIT_HMAC_KEY_FILE`: Path to a raw key file of at least 32 bytes. When neither this nor `GOSONIC_AUDIT_HMAC_KEY` is set, gosonic creates `~/.config/gosonic/audit.key`

Example using environment variables:
```bash
export SONIC_AUDIT_STORE=s3
export SONIC_AUDIT_S3_BUCKET=my-audit-logs
export SONIC_AUDIT_PATH=ci-logs/
gosonic ci run build
```

## Execution Variables

You can pass variables to be used during stage execution using the `--var` flag. Names that are valid environment variables (`AWS_PROFILE`, not `region.name`) are exported into the container when the stage does not already set that name. The shell expands `$NAME` or `${NAME}`. gosonic does not paste those values into `commands` or `steps.run`. Quote them in the script (`"$AWS_PROFILE"`). An unquoted expansion is still evaluated by the shell.

Names such as `region.name` are substituted into environment values, volume paths, image names, `when` expressions, and audit settings. They are not exported.

Do not pass secret values with `--var`. The value is visible in the shell history and in the gosonic process arguments. List the name under `secrets` and export the value in the environment that launches gosonic. See [Secrets](#secrets).

```bash
# Run deploy stage with specific region
gosonic ci run deploy --var region.name=us-east-1

# Multiple variables can be specified
gosonic ci run deploy --var region.name=us-east-1 --var env=prod
```

### Using Variables in Configuration

`${...}` is substituted in:
- Environment values
- Volume paths
- Runner and image names
- `when` expressions
- Audit bucket, prefix, and AWS profile

It is not substituted in `commands` or `steps.run`.

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

gosonic writes an audit record for each stage execution. `requires` trusts that record only when its signature verifies. Each record includes:
- Project and stage information
- Git revision
- Command executed, which is the `docker` argument list and does not include environment values
- Start time and duration
- Execution status and any errors
- HMAC signature over project, git revision, stage, command, start time, duration, status, and error

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
gosonic ci run build --audit-store=file --audit-path=.logs

# Using environment variables
export SONIC_AUDIT_STORE=s3
export SONIC_AUDIT_S3_BUCKET=my-audit-logs
export SONIC_AUDIT_PATH=ci-logs/
```

### Storage Options

#### File Store (Default)
- Type: `file`
- Stores logs as JSON files in a local directory
- Each file is mode `0600`
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
  - AWS credentials (see below)
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

### Signatures

Every stored record is signed with HMAC-SHA256. The signature covers the project, git revision, stage, command, start time, duration, status, and error. `LoadLogs` drops a record whose signature is missing or does not match, so a stage that can write the log directory cannot mint a `success` that satisfies `requires`.

Key selection:

1. `GOSONIC_AUDIT_HMAC_KEY`, base64, at least 32 bytes after decoding
2. `GOSONIC_AUDIT_HMAC_KEY_FILE`, a file of at least 32 raw bytes
3. Otherwise `~/.config/gosonic/audit.key`, created mode `0600` on first use and reused after that

The key is not copied into the container, and a bind of the key file or any directory that contains it is refused. Runs that check `requires` against records from another process need the same key. Set `GOSONIC_AUDIT_HMAC_KEY` from the CI secret store when the home directory is not shared. Records written by older versions of gosonic are unsigned and no longer satisfy `requires`.

Deleting a newer failure record still leaves an older signed success as the newest record for that stage. Signing does not prevent that.

### AWS Credentials for S3 Audit Logging

When using S3 for audit logging, gosonic looks for AWS credentials in the following order:

1. Audit-log specific profile:
   - `AUDIT_LOG_AWS_PROFILE` - Named profile from your AWS config

2. Audit-log specific credentials:
    - `AUDIT_LOG_AWS_ACCESS_KEY_ID`
    - `AUDIT_LOG_AWS_SECRET_ACCESS_KEY`
    - `AUDIT_LOG_AWS_SESSION_TOKEN` (optional)
    - `AUDIT_LOG_AWS_REGION`

3. Standard AWS credentials (if audit-log specific ones are not provided):
    - `AWS_ACCESS_KEY_ID`
    - `AWS_SECRET_ACCESS_KEY`
    - `AWS_SESSION_TOKEN` (optional)
    - `AWS_REGION` or `AWS_DEFAULT_REGION`

This allows you to use different AWS credentials for audit logging than for your task executions.

#### Configuring AWS Credentials

There are several ways to configure AWS credentials for audit logging:

1. **Using AWS profiles** (recommended):
   - Create a named profile in your `~/.aws/credentials` or `~/.aws/config` file
   - Set the `AUDIT_LOG_AWS_PROFILE` environment variable to use this profile
   
   ```ini
   # In ~/.aws/credentials
   [audit-profile]
   aws_access_key_id = AKIAXXXXXXXXXXXXXXXX
   aws_secret_access_key = XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
   region = us-west-2
   ```
   
   ```bash
   # Then use the profile
   export AUDIT_LOG_AWS_PROFILE=audit-profile
   ```

2. **Using environment variables**:
   - Set the audit-log specific environment variables
   
   ```bash
   export AUDIT_LOG_AWS_ACCESS_KEY_ID=AKIAXXXXXXXXXXXXXXXX
   export AUDIT_LOG_AWS_SECRET_ACCESS_KEY=XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
   export AUDIT_LOG_AWS_REGION=us-west-2
   ```

3. **Using IAM roles**:
   - When running in AWS (EC2, ECS, Lambda), you can use IAM roles
   - For audit logging, you can use a separate role by configuring a named profile that uses role assumption
   
   ```ini
   # In ~/.aws/config
   [profile audit-profile]
   role_arn = arn:aws:iam::123456789012:role/AuditLogRole
   source_profile = default
   region = us-west-2
   ```

4. **Using default credentials**:
   - If no audit-log specific credentials are provided, gosonic will use the default AWS credentials
   - This is useful when you want to use the same credentials for both task execution and audit logging

Examples:

Using a named profile:
```bash
# Use a specific profile for audit logging
export AUDIT_LOG_AWS_PROFILE=audit-profile
export AUDIT_LOG_AWS_REGION=us-west-2

# Run gosonic with S3 audit logging
gosonic ci run build --audit-store=s3 --audit-s3-bucket=my-audit-logs
```

Using explicit credentials:
```bash
# Set audit-log specific credentials
export AUDIT_LOG_AWS_ACCESS_KEY_ID=AKIAXXXXXXXXXXXXXXXX
export AUDIT_LOG_AWS_SECRET_ACCESS_KEY=XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
export AUDIT_LOG_AWS_REGION=us-west-2

# Run gosonic with S3 audit logging
gosonic ci run build --audit-store=s3 --audit-s3-bucket=my-audit-logs
```

### Configuration Priority

The audit store configuration is resolved in this order:
1. Command line flags
2. Environment variables
3. Configuration file
4. Defaults (file store in `.logs` directory)


## CI/CD Integration

gosonic is designed to be portable across different CI/CD platforms while maintaining consistent build behavior. Here are examples of how to integrate gosonic with popular CI/CD tools:

### GitHub Actions

```yaml
name: Build and Test
on: [push, pull_request]

jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      
      - name: Install gosonic
        run: |
          go install github.com/triha74/gosonic@latest
          
      - name: Run Tests
        run: gosonic ci run test
        
      - name: Build
        run: gosonic ci run build
        
      - name: Deploy (if main)
        if: github.ref == 'refs/heads/main'
        env:
          # Names listed under secrets: in the workflow. Values stay in the
          # Actions environment and are not written into the workflow file.
          AWS_ACCESS_KEY_ID: ${{ secrets.AWS_ACCESS_KEY_ID }}
          AWS_SECRET_ACCESS_KEY: ${{ secrets.AWS_SECRET_ACCESS_KEY }}
          # Same key on every job so a later job can verify requires.
          GOSONIC_AUDIT_HMAC_KEY: ${{ secrets.GOSONIC_AUDIT_HMAC_KEY }}
        run: gosonic ci run deploy
```

### GitLab CI

```yaml
image: golang:1.21

stages:
  - test
  - build
  - deploy

before_script:
  - go install github.com/triha74/gosonic@latest

test:
  stage: test
  script:
    - gosonic ci run test

build:
  stage: build
  script:
    - gosonic ci run build
  artifacts:
    paths:
      - bin/

deploy:
  stage: deploy
  script:
    - gosonic ci run deploy
  only:
    - main
  environment:
    name: production
```

### Jenkins Pipeline

```groovy
pipeline {
    agent any
    
    tools {
        go 'go-1.21'
    }
    
    stages {
        stage('Setup') {
            steps {
                sh 'go install github.com/triha74/gosonic@latest'
            }
        }
        
        stage('Test') {
            steps {
                sh 'gosonic ci run test'
            }
        }
        
        stage('Build') {
            steps {
                sh 'gosonic ci run build'
            }
        }
        
        stage('Deploy') {
            when {
                branch 'main'
            }
            steps {
                sh 'gosonic ci run deploy'
            }
        }
    }
}
```

### Azure DevOps

```yaml
trigger:
  - main

pool:
  vmImage: 'ubuntu-latest'

steps:
- task: Go@0
  inputs:
    command: 'get'
    arguments: 'github.com/triha74/gosonic@latest'
    
- script: gosonic ci run test
  displayName: 'Run Tests'
  
- script: gosonic ci run build
  displayName: 'Build'
  
- script: gosonic ci run deploy
  displayName: 'Deploy'
  condition: and(succeeded(), eq(variables['Build.SourceBranch'], 'refs/heads/main'))
```

### Benefits of gosonic in CI/CD

1. **Consistency Across Platforms**
   - Same build commands regardless of CI/CD platform
   - Identical build environment using Docker containers
   - Consistent artifact outputs

2. **Portable Configuration**
   - Workflow files under `.sonic/` define the build stages
   - No need to duplicate build logic across different CI/CD configs
   - Easy to switch between CI/CD providers

3. **Built-in Audit Trail**
   - Automatic logging of all build steps
   - HMAC-signed records, so `requires` does not trust a log the container wrote itself
   - Support for both local and S3-based audit logs
   - Consistent logging across all platforms

4. **Docker-based Isolation**
   - Clean build environment for every run
   - Containers run as the host user, with capabilities dropped
   - No dependency on CI/CD platform's installed tools
   - Reproducible builds across environments

5. **Matrix Build Support**
   - Run builds across multiple configurations
   - Consistent matrix syntax regardless of CI/CD platform
   - Easy to test against different versions/platforms

### Example: Matrix Builds

```yaml
# .sonic.yml
version: "1"
project:
  name: "my-app"
matrix:
  region:
    - name: "us-east-1"
      priority: 1
    - name: "eu-west-1"
      priority: 2
  environment:
    - name: "staging"
      priority: 1
    - name: "production"
      priority: 2

stages:
  deploy:
    runner: "aws-cli"
    matrix: ["region", "environment"]
    commands:
      - "aws deploy create-deployment --region $region --environment $environment"
```

Each cell exports the dimension values as environment variables. Run a subset with `--matrix`:

```bash
gosonic ci run deploy --matrix region=us-east-1,eu-west-1 --matrix environment=staging,production
```

## Security

gosonic runs untrusted commands inside a container, then trusts the audit record when a later stage says `requires`. The controls below are what that split depends on. They do not replace the CI system's own secret store or runner isolation.

### Secrets

Put secret values in the environment that starts gosonic, and list only the names in the workflow:

```yaml
stages:
  deploy:
    runner: aws-cli
    secrets:
      - AWS_ACCESS_KEY_ID
      - AWS_SECRET_ACCESS_KEY
    commands:
      - aws sts get-caller-identity
```

```bash
export AWS_ACCESS_KEY_ID=...
export AWS_SECRET_ACCESS_KEY=...
gosonic ci run deploy
```

gosonic reads each name from its own environment. A missing name fails the stage. The values are written to a temporary env file, mode `0600`, outside the repository, passed as `--env-file`, and deleted when the container exits. The logged `docker` command contains the file path, not the values. `environment:` entries and exported `--var` names travel in that same file.

`GOSONIC_AUDIT_HMAC_KEY` and `GOSONIC_AUDIT_HMAC_KEY_FILE` are never copied into that file, even if a stage lists them.

Do not put secret values in the workflow file. Do not pass them with `--var`: that puts the value in your shell history and in the arguments of the gosonic process. Non-secret settings such as `AWS_PROFILE` or `AWS_REGION` are fine as `--var` or `environment`.

A value you do put in `environment:` is not printed on the `docker` command line, but it is still stored in the workflow file. Use `secrets` for anything that must not be in git.

### Signed audit records

`requires` reads the newest audit record for a stage and accepts status `success` or `skipped`. Those records are HMAC-signed with a key the container does not receive. A stage that can write `.logs`, or any other audit directory inside the workspace, cannot forge a success that a later run will accept.

Details of the key and the signed fields are in [Signatures](#signatures). In short:

- Set `GOSONIC_AUDIT_HMAC_KEY` in CI so every job verifies records with the same key. The default file under `~/.config` is per machine and is a new key whenever that directory is empty.
- File records are mode `0600`.
- Unsigned records from older gosonic versions do not satisfy `requires`. Re-run those stages after upgrading.
- A container that can delete files can still remove a failure and leave an older signed success in place. Signing does not stop deletion.

### Container isolation

Each stage container is started with:

- `--user` set to the host uid and gid, unless the stage sets `user`
- `--cap-drop ALL`
- `--security-opt no-new-privileges`
- `--pids-limit 4096`, unless the stage sets `pids_limit`
- `--rm` and `--init`

There is no default memory limit. A compiler or test runner that needs more than 4096 processes can set `pids_limit`.

The workspace is mounted read-write so the stage can write build output. On top of a workspace bind, `.git` and `.sonic` are mounted read-only, so the container cannot install a git hook or rewrite the workflow that later stages will load. Cache and tmp volumes are unchanged.

`docker_socket: true` mounts the host Docker socket, which is root on the host for anyone who can use it. gosonic will not add that mount unless you pass `--allow-docker-socket` or set `GOSONIC_ALLOW_DOCKER_SOCKET` to `1` or `true`. When the mount is allowed, the socket's group is added so the non-root user can open it. A stage that is allowed the socket can read the signing key off the host filesystem. Treat that opt-in as a break in the audit signature guarantee. Writing the socket path yourself under `volumes` is not gated by the flag. Prefer `docker_socket`.

Service `ports` are published on `127.0.0.1` unless the spec already includes an address. `5432:5432` becomes `127.0.0.1:5432:5432`. `0.0.0.0:5432:5432` stays reachable on every interface.

The workspace mount is still the whole repository, read-write, except for `.git` and `.sonic`. A stage can write other files the next stage will compile. That is the build cache and the artifact path. Do not keep secrets in the repository.

### Images

Resolved image hosts must be one of `docker.io`, `public.ecr.aws`, `ghcr.io`, `quay.io`, or `localhost`. A port is allowed when the hostname is, so `localhost:5000/app:dev` is accepted. `allowed_registries` in the workflow adds hosts. It does not remove the built-in ones.

```yaml
allowed_registries:
  - registry.example.com
```

`--registry` and `GOSONIC_DEFAULT_REGISTRY` set the host used when an image name has none. That host is checked against the same list. An image that already names a registry is checked on its own host.

Images are not pulled by digest unless you write the digest. A reference without `@sha256:` prints a warning on stderr, once per image per process. `--require-image-digest` makes the missing digest an error. Tags such as `docker.io/library/docker:27-cli` keep working until you opt in.

gosonic does not verify image signatures. Pin a digest, or verify the image with your own tooling, when the registry is not a sufficient trust boundary.

### Artifact paths

After a successful stage, `artifacts` and `coverage.file` are read on the host from the workspace directory. Paths are opened relative to that directory. An absolute path, a `..` segment that leaves the workspace, or a symlink whose target leaves the workspace fails the stage. A container cannot point `dist` at `~/.aws` and have the host copy those credentials into `.sonic-artifacts`.

```yaml
stages:
  test:
    artifacts:
      - cover.out
    coverage:
      enabled: true
      file: cover.out
      format: percent
      threshold: 80
```

## Development

Requirements:
- Go 1.24 or later
- Docker

### Building from Source

```bash
# Clone the repository
git clone https://github.com/yourusername/go-sonic.git
cd go-sonic

# Clean, vendor, test, build, and install
make
```

Targets: `clean`, `tidy`, `vendor`, `test`, `build`, and `install`. `make` runs them in that order.

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
