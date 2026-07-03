# AGENTS.md

Guidance for AI agents (Claude Code, etc.) working in this repository.

## Project Overview

sitectl is a Go CLI tool for managing local and remote Docker Compose sites. It wraps Docker Compose commands with context-aware configuration, enabling seamless interaction with both local and SSH-connected remote Docker environments. The config command structure is heavily inspired by kubectl's context management.

## Build, Test, and Lint Commands

```bash
# Install dependencies and build
make build

# Run linter (includes gofmt and golangci-lint)
make lint

# Run all tests with race detection
make test

# Run a specific test
go test -v -race ./pkg/config -run TestSpecificTestName

# Run tests for a single package
go test -v -race ./pkg/config

# Use Go workspaces to develop against local plugin repos
make work
```

## Architecture

### Context System

The core abstraction is the **Context**, which represents a Docker Compose site running either locally or remotely:

- **Local contexts** (`type: local`): Docker running on the same machine
- **Remote contexts** (`type: remote`): Docker on a remote server accessed via SSH

Contexts are stored in `~/.sitectl/config.yaml` and include:
- Docker socket path
- Compose project name and profile
- Project directory
- For remote: SSH connection details (hostname, user, port, key path)
- Environment file paths

### Command Flow

1. Commands accept a global `--context` flag to override the current context
2. The context determines how Docker commands are executed:
   - Local: Direct Docker socket connection
   - Remote: SSH tunnel to remote Docker socket
3. Docker Compose commands automatically inject `--profile` and `--env-file` flags based on context

### Key Packages

**`pkg/config`**: Context management and configuration loading
- `Context` struct: Complete context definition with connection details
- `Load()`/`Save()`: YAML-based config persistence
- `CurrentContext()`: Resolves active context from flags or config
- `DialSSH()`: Establishes SSH connections for remote contexts
- `ReadSmallFile()`: Abstraction for reading files locally or over SSH
- `RunCommand()`: Executes commands locally or over SSH based on context

**`pkg/docker`**: Docker client abstraction
- `DockerClient`: Wraps Docker API, works over local socket or SSH tunnel
- `GetDockerCli()`: Factory that creates appropriate client based on context type
- `GetContainerName()`: Resolves service names to container names using Compose labels
- `GetServiceIp()`: Gets container IP within Docker network
- `ExecCapture()`: Executes a container command and captures stdout; use this instead of rolling your own capture wrapper

**`pkg/job`**: Shared job utilities for plugins
- `ConfirmDatabaseReplacement()`: Prompts user before destructive DB imports (with `--yolo` bypass)
- `ResolveRecentArtifact()`: Resolves or produces a dated artifact (today/yesterday reuse)
- `StageArtifactBetweenContexts()`: Downloads from source context, uploads to target
- `DownloadContextFile()` / `EnsurePathAbsentOnContext()` / `EnsureDirOnContext()`: File transfer helpers

**`pkg/plugin`**: Plugin SDK and discovery
- `SDK`: Entry point for plugin authors; provides context, Docker client, job registration
- `debugui`: Canonical debug panel renderer — use `debugui.RenderPanel`, `debugui.FormatRows`, etc.; do not copy these locally

**`pkg/helpers`**: Shared CLI utilities
- `FirstNonEmpty()`: Returns first non-empty string from a variadic list
- `GetContextFromArgs()`: Extracts `--context` flag from `DisableFlagParsing` commands

**`cmd/`**: Cobra command definitions
- `compose.go`: Wraps Docker Compose with automatic flag injection
- `port-forward.go`: SSH port forwarding to remote container IPs
- `config.go`: Context CRUD operations

### Remote Context Implementation

Remote contexts use SSH as a transport layer:
1. SSH connection established via `Context.DialSSH()` using key-based auth
2. Docker API calls tunneled through SSH to remote socket
3. HTTP transport uses SSH connection's `Dial()` for Unix socket forwarding
4. File operations (reading .env, secrets) execute via SSH session or SFTP

### Authentication

- `sitectl login`: Opens browser for authentication (Google OAuth or email/password)
- `sitectl logout`: Removes stored credentials
- `sitectl whoami`: Shows current authentication status
- Token storage: `~/.sitectl/oauth.json` with 0600 permissions

## Go Coding Conventions

### Core Principles

- **Simplicity First:** Favor simple, readable code over clever solutions
- **Idiomatic Go:** Follow standard Go conventions and community practices
- **Standard Library:** Prefer the Go standard library over third-party dependencies

### Code Style

- Follow all conventions outlined in [Effective Go](https://go.dev/doc/effective_go)
- Use `gofmt` to format all code before committing
- Keep functions small and focused on a single responsibility
- Create utility functions for any behavior that repeats more than twice
- Name variables clearly; avoid abbreviations unless universally understood (e.g., `i` for index)

### Naming Conventions

- **Packages:** Short, concise, lowercase, single-word names
- **Interfaces:** Use `-er` suffix for single-method interfaces (e.g., `Reader`, `Writer`)
- **Getters:** Omit `Get` prefix (use `Name()`, not `GetName()`)
- **Acronyms:** Keep consistent case (e.g., `userID`, `HTTPServer`, not `userId`, `HttpServer`)

### Dependency Management

- Default to the standard library; only introduce external dependencies when necessary
- Prefer `net/http` for routing (Go 1.22+ has built-in advanced routing)
- Document why any external dependency is required

### Error Handling

- Always check and handle errors explicitly
- Use `RunE` (not `Run`) for Cobra commands so errors propagate correctly
- Return errors rather than calling `os.Exit` or `log.Fatal` outside of `main`/`Execute`
- Wrap errors with context: `fmt.Errorf("context: %w", err)`
- Don't ignore errors with `_` without a clear reason
- Output to `cmd.OutOrStdout()` / `cmd.ErrOrStderr()`, not `fmt.Println` / `os.Stdout`

```go
// Good
if err := doSomething(); err != nil {
    return fmt.Errorf("failed to do something: %w", err)
}
```

### Plugin/SDK Reuse

- Before writing a helper in a plugin, check `pkg/docker`, `pkg/job`, `pkg/helpers`, and `pkg/plugin/debugui`
- Do not copy debug panel styles or render functions locally — use `debugui`
- Do not copy `ExecCapture` — use `docker.ExecCapture`
- Do not copy `ConfirmDatabaseReplacement` — use `job.ConfirmDatabaseReplacement`
- Follow the Go idiom: a little copying is better than a little abstraction, but we already have the dependency

### Compose YAML Mutation

- Treat Docker Compose files as user-authored documents, not just YAML data. The generic YAML AST encoder rewrites anchors, comments, folded scalars, quote style, and command blocks; for example it can collapse a readable `command: >-` block into one long line or convert it to a sequence. That creates noisy diffs and can change runtime behavior.
- Use the text-preserving helpers in `pkg/component/compose_file.go` for Docker Compose mutations whenever possible (`LoadComposeFile`, `SetServiceEnv`, `AppendUniqueServiceString`, `RemoveServiceString`, service/volume block helpers). Add helpers there when a compose mutation repeats instead of reaching for `LoadYAMLDocument`.
- Reserve `pkg/component/yaml_mutator.go` for non-compose YAML or cases where full document reserialization is acceptable. If a change touches `docker-compose*.yml`, tests must assert that anchors, comments, and folded scalar command formatting stay stable.

### Go Workspaces

- **Never use `replace` directives** in `go.mod` for local development
- Use `make work` (runs `scripts/use-go-work.sh`) to create a `go.work` file instead

### Concurrency

- Prefer channels for communication between goroutines
- Avoid shared mutable state; use `sync.Mutex` when necessary
- Always run tests with `-race`: `go test -race ./...`
- Use `context.Context` for cancellation and timeout control

### Logging

- Use `log/slog` for all structured logging
- Log levels: Debug (diagnostics), Info (general), Warn (potentially harmful), Error (failures)
- Never log sensitive data (passwords, tokens, secrets)

```go
slog.Info("user authenticated", "user_id", userID, "ip_address", ipAddr)
```

### Testing

- Write unit tests for all new features and bug fixes
- Use table-driven tests for multiple scenarios
- Use `t.Helper()` in test helper functions
- Run with race detection: `go test -race ./...`

```go
func TestCalculate(t *testing.T) {
    tests := []struct {
        name    string
        input   int
        want    int
        wantErr bool
    }{
        {"positive number", 5, 25, false},
        {"zero", 0, 0, false},
        {"negative number", -5, 0, true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := Calculate(tt.input)
            if (err != nil) != tt.wantErr {
                t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            if got != tt.want {
                t.Errorf("got %v, want %v", got, tt.want)
            }
        })
    }
}
```

### Documentation

- Every exported function, type, method, and package must have a comment
- Start comments with the name of the element being documented
- Comment the **why**, not the **what** for internal logic

```go
// UserService handles all user-related operations.
type UserService struct{}

// GetUser retrieves a user by their ID.
func (s *UserService) GetUser(id string) (*User, error) {}
```

### Linting

- Use `golangci-lint` for all linting checks
- Fix all linting issues before committing
- Run `make lint` before pushing

## Development Notes

- Uses `charmbracelet/fang` for command execution (replaces standard Cobra `Execute`)
- Logging via `slog` with level controlled by `LOG_LEVEL` env var or `--log-level` flag
- The `compose` command uses `DisableFlagParsing: true` to pass all flags through to Docker Compose; use `helpers.GetContextFromArgs` to extract `--context` in those commands
- SSH known_hosts validation is enforced for remote connections
- Context names are case-insensitive when searching
