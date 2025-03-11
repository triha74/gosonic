# Hello World Example

This is a simple example showing how to use go-sonic to build and test a Go application.

## Running the Example

```bash
# Run all stages
gosonic run lint test build run

# Run individual stages
gosonic run test
gosonic run build

# Run the application
gosonic run run
```

## Stage Details

- `lint`: Runs golangci-lint for code quality checks
- `test`: Runs unit tests
- `build`: Builds the binary
- `run`: Runs the built binary

## Project Structure

```
.
├── .sonic.yml      # go-sonic configuration
├── main.go         # Application code
├── main_test.go    # Unit tests
└── README.md       # This file
``` 