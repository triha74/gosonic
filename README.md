# GoSonic - Docker-based CI/CD Task Runner

# gosonic

A modern, fully portable, Docker-based CI/CD task execution tool written in Go.

## Overview

gosonic is a Docker-based task execution tool that provides a unified approach to building, testing, packaging, and deploying applications. While it shares configuration similarities with CI/CD platforms like GitHub Actions, gosonic is specifically designed as a modern replacement for traditional build tools like `make`.

Key distinctions:
- **Task Executor, Not Pipeline Tool**: gosonic executes tasks within CI/CD pipelines rather than replacing the pipeline itself
- **Docker-First Design**: All tasks run in isolated Docker containers, ensuring consistent execution environments
- **Platform Agnostic**: Works identically across different CI/CD platforms (GitHub Actions, GitLab CI, Jenkins, etc.)
- **Simple Configuration**: Uses a single YAML file to define execution stages and their environments

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
   - [Project Configuration](#project-configuration)
   - [Stage Configuration](#stage-configuration)
   - [Runner Configuration](#runner-configuration)
   - [Volume Configuration](#volume-configuration)
4. [Command Line Interface](#command-line-interface)
   - [Environment Variables](#environment-variables)
5. [Execution Variables](#execution-variables)
   - [Using Variables in Configuration](#using-variables-in-configuration)
6. [Audit Logging](#audit-logging)
   - [Configuration](#audit-configuration)
   - [AWS Credentials for S3 Audit Logging](#aws-credentials-for-s3-audit-logging)
7. [CI/CD Integration](#cicd-integration)
   - [GitHub Actions](#github-actions)
   - [GitLab CI](#gitlab-ci)
   - [Azure DevOps](#azure-devops)
   - [Benefits of gosonic in CI/CD](#benefits-of-gosonic-in-cicd)
8. [Security Considerations](#security-considerations)
9. [Development](#development)
   - [Building from Source](#building-from-source)
   - [Running Tests](#running-tests)
10. [License](#license)

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
- `GOSONIC_DEFAULT_REGISTRY`: Default Docker registry
- `GOSONIC_VERBOSE`: Enable verbose output

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
gosonic run build --audit-store=s3 --audit-s3-bucket=my-audit-logs
```

Using explicit credentials:
```bash
# Set audit-log specific credentials
export AUDIT_LOG_AWS_ACCESS_KEY_ID=AKIAXXXXXXXXXXXXXXXX
export AUDIT_LOG_AWS_SECRET_ACCESS_KEY=XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
export AUDIT_LOG_AWS_REGION=us-west-2

# Run gosonic with S3 audit logging
gosonic run build --audit-store=s3 --audit-s3-bucket=my-audit-logs
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
        run: gosonic run test
        
      - name: Build
        run: gosonic run build
        
      - name: Deploy (if main)
        if: github.ref == 'refs/heads/main'
        env:
          AWS_PROFILE: ${{ secrets.AWS_PROFILE }}
          AWS_REGION: ${{ secrets.AWS_REGION }}
        run: gosonic run deploy
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
    - gosonic run test

build:
  stage: build
  script:
    - gosonic run build
  artifacts:
    paths:
      - bin/

deploy:
  stage: deploy
  script:
    - gosonic run deploy
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
                sh 'gosonic run test'
            }
        }
        
        stage('Build') {
            steps {
                sh 'gosonic run build'
            }
        }
        
        stage('Deploy') {
            when {
                branch 'main'
            }
            steps {
                sh 'gosonic run deploy'
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
    
- script: gosonic run test
  displayName: 'Run Tests'
  
- script: gosonic run build
  displayName: 'Build'
  
- script: gosonic run deploy
  displayName: 'Deploy'
  condition: and(succeeded(), eq(variables['Build.SourceBranch'], 'refs/heads/main'))
```

### Benefits of gosonic in CI/CD

1. **Consistency Across Platforms**
   - Same build commands regardless of CI/CD platform
   - Identical build environment using Docker containers
   - Consistent artifact outputs

2. **Portable Configuration**
   - Single `.sonic.yml` file defines all build stages
   - No need to duplicate build logic across different CI/CD configs
   - Easy to switch between CI/CD providers

3. **Built-in Audit Trail**
   - Automatic logging of all build steps
   - Support for both local and S3-based audit logs
   - Consistent logging across all platforms

4. **Docker-based Isolation**
   - Clean build environment for every run
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
    commands:
      - "aws deploy create-deployment --region ${region.name} --environment ${environment.name}"
```

This can be executed in any CI/CD platform:

```bash
gosonic run deploy --matrix region=us-east-1,eu-west-1 --matrix environment=staging,production
```

### Security Considerations

1. **Secrets Management**
   - Use CI/CD platform's native secrets management
   - Pass secrets as environment variables to gosonic
   - Avoid hardcoding sensitive values in `.sonic.yml`

2. **Artifact Security**
   - Use read-only volume mounts for sensitive files
   - Leverage CI/CD platform's artifact encryption
   - Enable audit logging for traceability

3. **Access Control**
   - Use minimal required permissions for service accounts
   - Leverage CI/CD platform's RBAC features
   - Implement stage-specific access controls

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
