# AWS CLI Example

This example runs the AWS CLI in a container. `.sonic.yml` writes audit records to `.logs`. `.sonic-s3-audit.yml` writes them to S3 and can use a different AWS account for that bucket.

## What the stages do

Both files define `identity`, `s3-list`, and `ec2-describe`. Each stage:

- Uses the `aws-cli/aws-cli` image, resolved under the default registry
- Bind-mounts `${HOME}/.aws/` at `/root/.aws/` so the CLI can read a named profile
- Leaves `${AWS_PROFILE}` in the command for the shell to expand

`${HOME}` in the volume source is substituted from `--var`. Each stage sets `HOME` to `/root`, so that value is used for the mount and the CLI still reads `/root/.aws`. `${AWS_PROFILE}` in the command is not substituted by gosonic. Pass it with `--var` so it is exported into the container, and keep the command quoted if you change it.

Do not mount `$HOME` itself. The audit signing key lives under the home directory, and gosonic refuses a bind that would expose it.

## Run a stage

```bash
gosonic --var AWS_PROFILE=your-profile --var HOME=${HOME} run identity
gosonic --var AWS_PROFILE=your-profile --var HOME=${HOME} run s3-list
gosonic --var AWS_PROFILE=your-profile --var AWS_REGION=us-west-2 --var HOME=${HOME} run ec2-describe
```

`ec2-describe` uses `${AWS_REGION:-us-east-1}` inside the shell, so the region falls back to `us-east-1` when `AWS_REGION` is unset.

These stages authenticate with the mounted profile. They do not read `AWS_ACCESS_KEY_ID` from the host unless you list that name under `secrets`.

## Passing key material into the container

Export the values in the environment that starts gosonic and name them in the stage. The values are not written to the workflow file, the `docker` command line, or the audit record.

```yaml
secrets:
  - AWS_ACCESS_KEY_ID
  - AWS_SECRET_ACCESS_KEY
```

```bash
export AWS_ACCESS_KEY_ID=AKIA...
export AWS_SECRET_ACCESS_KEY=...
```

Do not pass those values with `--var`. That leaves them in shell history and in the gosonic process arguments. `AWS_PROFILE` and `AWS_REGION` are not secrets and are fine as `--var`.

## S3 audit logging

`.sonic-s3-audit.yml` sets `audit.store` to `s3`. `s3bucket` and `aws_profile` are substituted from `--var` because they are audit settings, not commands.

```bash
gosonic --sonic-file .sonic-s3-audit.yml \
  --var S3_BUCKET=my-audit-logs \
  --var AWS_PROFILE=audit-profile \
  --var HOME=${HOME} \
  run identity
```

Audit credentials are separate from the credentials the stage uses. gosonic looks for them in this order:

1. `AUDIT_LOG_AWS_PROFILE`
2. `AUDIT_LOG_AWS_ACCESS_KEY_ID`, `AUDIT_LOG_AWS_SECRET_ACCESS_KEY`, and optional `AUDIT_LOG_AWS_SESSION_TOKEN`
3. The default AWS credential chain

`AUDIT_LOG_AWS_SECRET_ACCESS_KEY` is used by gosonic to upload the log. It is not copied into the stage container. The signing key is also kept out of the container. Set `GOSONIC_AUDIT_HMAC_KEY` when a later run must verify these records and does not share `~/.config/gosonic/audit.key`.

Records are HMAC-signed. A container that can write the local log directory, or the S3 prefix, cannot forge a `success` that `requires` will accept without that key.
