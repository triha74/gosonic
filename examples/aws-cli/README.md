# AWS CLI Example

This example demonstrates how to use gosonic with the AWS CLI, including separate credentials for audit logging.

## Configuration

The `.sonic.yml` file configures:
1. A stage that lists S3 buckets using the AWS CLI
2. S3-based audit logging

## Usage

### With the same AWS credentials for both operations

```bash
# Set AWS credentials
export AWS_ACCESS_KEY_ID=AKIAXXXXXXXXXXXXXXXX
export AWS_SECRET_ACCESS_KEY=XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
export AWS_REGION=us-east-1

# Run the stage
gosonic run list-buckets
```

### With separate credentials for audit logging

#### Using explicit credentials
```bash
# Set AWS credentials for the AWS CLI command
export AWS_ACCESS_KEY_ID=AKIAXXXXXXXXXXXXXXXX
export AWS_SECRET_ACCESS_KEY=XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
export AWS_REGION=us-east-1

# Set separate credentials for audit logging
export AUDIT_LOG_AWS_ACCESS_KEY_ID=AKIAYYYYYYYYYYYYYYY
export AUDIT_LOG_AWS_SECRET_ACCESS_KEY=YYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYY
export AUDIT_LOG_AWS_REGION=us-west-2

# Run the stage
gosonic run list-buckets
```

#### Using a named profile
```bash
# Set AWS credentials for the AWS CLI command
export AWS_ACCESS_KEY_ID=AKIAXXXXXXXXXXXXXXXX
export AWS_SECRET_ACCESS_KEY=XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
export AWS_REGION=us-east-1

# Use a specific profile for audit logging
export AUDIT_LOG_AWS_PROFILE=audit-profile
export AUDIT_LOG_AWS_REGION=us-west-2

# Run the stage
gosonic run list-buckets
```

This setup allows you to use different AWS accounts or IAM roles for:
1. The actual AWS operations performed by your stages
2. The audit logging functionality

This is useful for security separation or when your audit logs should be stored in a different AWS account.

## Prerequisites

- AWS CLI credentials configured in your `~/.aws/` directory
- An AWS profile set up (or use the default profile)

## Running the Example

To run the example with a specific AWS profile:

```bash
# Get your AWS identity
gosonic --var AWS_PROFILE=your-profile --var HOME=${HOME} run identity

# List S3 buckets
gosonic --var AWS_PROFILE=your-profile --var HOME=${HOME} run s3-list

# Describe EC2 instances in a specific region
gosonic --var AWS_PROFILE=your-profile --var AWS_REGION=us-west-2 --var HOME=${HOME} run ec2-describe
```

## How It Works

This example:

1. Uses the official AWS CLI Docker image as the runner
2. Mounts your local AWS credentials into the container
3. Passes your AWS profile to the commands
4. Demonstrates variable substitution in commands

## Notes

- The `${AWS_PROFILE}` variable is resolved at runtime from the `--var` flag
- The `${HOME}` variable is automatically resolved to your home directory
- You can specify a region with the `AWS_REGION` variable, or it defaults to us-east-1 