# Hello World Example

This example builds and runs a small Go program. Each stage runs in a container as the host user, with capabilities dropped. `build` and `execute` use `requires`, which accepts only a signed successful audit record from the earlier stage.

## Running the Example

```bash
# Run the stages in dependency order
gosonic run test build execute

# Run one stage. build fails until test has a signed success for this key
gosonic run test
gosonic run build

# Run the binary built by the build stage
gosonic run execute
```

`clean` removes `bin/` and `.logs`.

## Stage Details

- `clean`: removes build output and local audit files
- `test`: runs `go test`
- `build`: builds `bin/hello-world` after `test` has succeeded
- `execute`: runs that binary after `test` and `build`

Audit records are written to `.logs` and signed with `~/.config/gosonic/audit.key` unless `GOSONIC_AUDIT_HMAC_KEY` or `GOSONIC_AUDIT_HMAC_KEY_FILE` is set.

## Project Structure

```
.
├── .sonic.yml      # Workflow
├── main.go         # Application code
├── main_test.go    # Unit tests
└── README.md       # This file
```
