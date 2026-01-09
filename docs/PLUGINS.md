# Sitectl Plugin System

Complete guide to developing, distributing, and using sitectl plugins.

---

**Table of Contents**
- [Quick Start](#quick-start)
- [Plugin Development](#plugin-development)
- [Docker Integration](#docker-integration)
- [Architecture](#architecture)
- [Distribution](#distribution)

---

## Quick Start

### TL;DR

```bash
# Create a new plugin
./scripts/create-plugin.sh awesome

# Edit the plugin
vim plugins/awesome/main.go

# Build it
make awesome

# Install it
make install-plugin-awesome

# Use it
sitectl awesome --help
```

### Minimal Plugin Example

```go
package main

import (
    "fmt"
    "github.com/libops/sitectl/pkg/plugin"
    "github.com/spf13/cobra"
)

func main() {
    sdk := plugin.NewSDK(plugin.Metadata{
        Name:        "awesome",
        Version:     "1.0.0",
        Description: "An awesome plugin",
        Author:      "You",
    })

    // Required for plugin discovery
    sdk.AddCommand(sdk.GetMetadataCommand())

    // Your command
    cmd := &cobra.Command{
        Use:   "do-thing",
        Short: "Does a thing",
        Run: func(cmd *cobra.Command, args []string) {
            fmt.Println("Thing done!")
        },
    }
    sdk.AddCommand(cmd)

    sdk.Execute()
}
```

Save as `plugins/awesome/main.go`, then:

```bash
make awesome
./sitectl-awesome do-thing
```

---

## Plugin Development

### Creating a Plugin

**Option 1: Use the generator**
```bash
./scripts/create-plugin.sh myplugin
```

**Option 2: Copy an existing plugin**
```bash
cp -r plugins/isle plugins/myplugin
# Edit plugins/myplugin/main.go
```

### Project Structure

```
plugins/myplugin/
├── main.go              # Entry point (required)
├── cmd/                 # Commands (optional)
│   └── *.go
├── pkg/                 # Packages (optional)
│   └── *.go
├── Makefile            # Build automation (for standalone repo)
├── go.mod              # Go module
└── README.md           # Documentation (for standalone repo)
```

### Using the Plugin SDK

The SDK provides common functionality:

```go
import "github.com/libops/sitectl/pkg/plugin"

sdk := plugin.NewSDK(plugin.Metadata{
    Name:        "myplugin",
    Version:     "1.0.0",
    Description: "My plugin description",
    Author:      "Your Name",
})

// Standard flags (--log-level, --format) are automatic

// Add custom commands
myCmd := &cobra.Command{
    Use:   "subcommand",
    Short: "Description",
    RunE: func(cmd *cobra.Command, args []string) error {
        // Your logic here
        return nil
    },
}
sdk.AddCommand(myCmd)

// Execute
sdk.Execute()
```

### Common Patterns

**Command with flags:**
```go
cmd := &cobra.Command{
    Use:   "list",
    Short: "List things",
    Run: func(cmd *cobra.Command, args []string) {
        verbose, _ := cmd.Flags().GetBool("verbose")
        if verbose {
            fmt.Println("Verbose output...")
        }
    },
}
cmd.Flags().BoolP("verbose", "v", false, "Verbose output")
```

**Command with arguments:**
```go
cmd := &cobra.Command{
    Use:   "greet <name>",
    Short: "Greet someone",
    Args:  cobra.ExactArgs(1),
    Run: func(cmd *cobra.Command, args []string) {
        name := args[0]
        fmt.Printf("Hello, %s!\n", name)
    },
}
```

**Error handling:**
```go
cmd := &cobra.Command{
    Use:   "risky",
    Short: "Might fail",
    RunE: func(cmd *cobra.Command, args []string) error {
        if err := doSomething(); err != nil {
            return fmt.Errorf("failed: %w", err)
        }
        return nil
    },
}
```

### Plugin Naming Convention

- **Binary name**: `sitectl-<name>`
- **Plugin name**: The part after `sitectl-`
- **Command usage**: `sitectl <name> [subcommand]`

Example:
- Binary: `sitectl-isle`
- Plugin name: `isle`
- Usage: `sitectl isle migrate-legacy`

### Plugin Discovery

Plugins are discovered by scanning all directories in your `PATH` for binaries matching `sitectl-*` (excluding `sitectl` itself).

**Common installation locations:**
- `/usr/local/bin/` (system-wide)
- `~/.local/bin/` (user-specific)
- Any directory in your `PATH`

---

## Docker Integration

### Overview

Plugins can easily interact with Docker containers (local or remote) using the SDK's built-in helpers. The SDK handles context management, SSH tunneling, and connection logic automatically.

### Quick Example: Drupal Plugin

```go
package main

import (
    "github.com/libops/sitectl/pkg/config"
    "github.com/libops/sitectl/pkg/plugin"
    "github.com/spf13/cobra"
)

func main() {
    sdk := plugin.NewSDK(plugin.Metadata{
        Name:        "drupal",
        Version:     "1.0.0",
        Description: "Drupal utilities",
    })

    currentContext, _ := config.Current()
    sdk.AddLibopsFlags(currentContext)
    sdk.AddCommand(sdk.GetMetadataCommand())

    drushCmd := &cobra.Command{
        Use:   "drush [args...]",
        Short: "Run drush commands in the Drupal container",
        RunE: func(cmd *cobra.Command, args []string) error {
            // Works locally or remotely!
            exitCode, err := sdk.ExecInContainer(cmd.Context(), "drupal",
                append([]string{"drush"}, args...))
            if err != nil {
                return err
            }
            return nil
        },
    }

    sdk.AddCommand(drushCmd)
    sdk.Execute()
}
```

**Usage:**
```bash
# Local development
$ sitectl drupal drush status

# Remote production (automatically via SSH!)
$ sitectl --context production drupal drush status
```

### SDK Docker Helpers

**Get a Docker client:**
```go
// Respects current context (local or remote)
// Automatically handles SSH tunneling for remote contexts
cli, err := sdk.GetDockerClient()
if err != nil {
    return err
}
defer cli.Close()

// Use the client - it wraps the standard Docker client
containers, err := cli.CLI.ContainerList(ctx, container.ListOptions{})
```

**Execute commands in containers:**
```go
// Simple execution (stdout/stderr to terminal)
exitCode, err := sdk.ExecInContainer(ctx, "my-container",
    []string{"ls", "-la", "/app"})

// Interactive with TTY (for shells)
exitCode, err := sdk.ExecInContainerInteractive(ctx, "my-container",
    []string{"/bin/bash"})
```

**Access context configuration:**
```go
context, err := sdk.GetContext()
if err != nil {
    return err
}

fmt.Printf("Context: %s\n", context.Name)
fmt.Printf("Type: %s\n", context.DockerHostType)

if context.IsRemote() {
    fmt.Printf("SSH: %s@%s\n", context.SSHUser, context.SSHHostname)
}
```

### Using Docker Package Directly

For more control, use the docker package directly:

```go
import (
    "github.com/libops/sitectl/pkg/docker"
    "github.com/libops/sitectl/pkg/config"
)

// Get context
ctx, err := config.GetContext("production")
if err != nil {
    return err
}

// Create client - handles local and remote (SSH) automatically
cli, err := docker.GetDockerCli(ctx)
if err != nil {
    return err
}
defer cli.Close()

// Use standard Docker API via cli.CLI
containers, err := cli.CLI.ContainerList(context.Background(), container.ListOptions{})

// Execute commands with advanced options
exitCode, err := cli.Exec(context.Background(), docker.ExecOptions{
    Container:    "my-container",
    Cmd:          []string{"php", "artisan", "migrate"},
    WorkingDir:   "/var/www/html",
    User:         "www-data",
    Env:          []string{"APP_ENV=production"},
    AttachStdout: true,
    AttachStderr: true,
})

// Or use convenience methods
exitCode, err := cli.ExecSimple(ctx, "my-container", []string{"ls", "-la"})
exitCode, err := cli.ExecInteractive(ctx, "my-container", []string{"/bin/bash"})
```

### Context Setup

Users set up contexts for different environments:

```bash
# Local development
$ sitectl config set-context local \
  --type local \
  --docker-socket /var/run/docker.sock

# Remote production
$ sitectl config set-context production \
  --type remote \
  --ssh-hostname prod.example.com \
  --ssh-user deploy \
  --ssh-key ~/.ssh/id_rsa

# Use a context
$ sitectl config use-context production
```

Plugins automatically use the current context when using SDK helpers.

---

## Architecture

### How It Works

1. Main CLI starts and calls `discoverAndRegisterPlugins()`
2. Scans all directories in `$PATH`
3. Finds binaries prefixed with `sitectl-` (excluding `sitectl` itself)
4. Registers each as a cobra subcommand with `DisableFlagParsing: true`
5. When invoked, uses `syscall.Exec` to replace current process with plugin binary

**Example:**
```
Binary: /usr/local/bin/sitectl-isle
Command: sitectl isle [subcommands...]
```

### Plugin SDK Features

**Metadata Support:**
```go
sdk := plugin.NewSDK(plugin.Metadata{
    Name:        "myplugin",
    Version:     "1.0.0",
    Description: "What this does",
    Author:      "Your Name",
})
```

**Standard Flags:**
- `--log-level` (DEBUG, INFO, WARN, ERROR)
- `--format` (table, json, or custom template)
- `--version` (plugin version)

**Libops Integration:**
```go
sdk.AddLibopsFlags(currentContext)
// Adds: --context, --api-url
```

**Docker Integration:**
- `sdk.GetDockerClient()` - Get Docker client (handles local/remote via SSH)
- `sdk.GetContext()` - Get context config
- `sdk.ExecInContainer()` - Execute command in container
- `sdk.ExecInContainerInteractive()` - Interactive exec with TTY

### Design Benefits

**For Plugin Developers:**
- Easy to get started
- No code duplication
- Consistent UX
- Simple build process
- Full Go power

**For Users:**
- Easy installation
- Seamless integration
- No restarts needed
- Clear separation

**For Maintainers:**
- Modular codebase
- Reduced main binary size
- Easier testing
- Backward compatibility

---

## Distribution

### Building

```bash
# Build for current platform
make build

# Build for all platforms
make build-all

# Build for specific platform
make build-linux
make build-darwin
make build-windows
```

Binaries are placed in `bin/` directory.

### Installation

**Manual installation:**

1. Download the binary for your platform
2. Rename to `sitectl-<name>` (if needed)
3. Make it executable: `chmod +x sitectl-<name>`
4. Move to PATH: `mv sitectl-<name> /usr/local/bin/`

**From source:**

```bash
git clone https://github.com/libops/sitectl-<name>.git
cd sitectl-<name>
make install
```

### Publishing to GitHub

Each plugin should be its own repository:

**Repository structure:**
```
sitectl-<name>/
├── cmd/                 # Command implementations
├── main.go             # Entry point
├── Makefile           # Build automation
├── go.mod             # Dependencies
├── README.md          # Documentation
├── CONTRIBUTING.md    # Contribution guide
├── LICENSE            # License file
└── .gitignore         # Git ignore
```

**Repository naming:**
- Format: `github.com/libops/sitectl-<name>`
- Examples:
  - `github.com/libops/sitectl-isle`
  - `github.com/libops/sitectl-libops`

**Release process:**

1. Tag a release: `git tag v1.0.0`
2. Push tags: `git push --tags`
3. Build binaries: `make build-all`
4. Create GitHub release with binaries

---

## Best Practices

1. **Follow the naming convention**: Use `sitectl-<name>`
2. **Include metadata command**: For plugin discovery
3. **Use the SDK**: Leverage shared functionality
4. **Handle errors gracefully**: Use `RunE` not `Run`
5. **Support standard flags**: `--log-level`, `--format`, `--context`
6. **Document your commands**: Good `Short` and `Long` descriptions
7. **Version semantically**: Follow semver (1.0.0, 1.1.0, etc.)
8. **Write tests**: Unit tests for your logic
9. **Respect contexts**: Use SDK helpers for Docker operations
10. **Keep it focused**: One plugin, one purpose

---

## Examples

### Available Plugins

**[sitectl-isle](https://github.com/libops/sitectl-isle)**
- Islandora (ISLE) migration tools
- Migrate legacy compose files to unified format
- Example: Domain-specific utility plugin

**[sitectl-libops](https://github.com/libops/sitectl-libops)**
- Libops API integration
- Manage organizations, projects, sites
- Example: Complex multi-command plugin

### Plugin Ideas

- `sitectl-drupal` - Drupal development utilities (drush, cache, shell)
- `sitectl-wordpress` - WordPress CLI integration
- `sitectl-db` - Database utilities (backup, restore, shell)
- `sitectl-deploy` - Deployment utilities
- `sitectl-monitor` - Monitoring and health checks

---

## Troubleshooting

### Plugin not discovered

```bash
# Check if binary is in PATH
which sitectl-myplugin

# Verify it's executable
ls -l $(which sitectl-myplugin)

# Check PATH
echo $PATH
```

### Build errors

```bash
# Clean and rebuild
go clean
go mod tidy
make build
```

### Import errors

```bash
# From plugin directory
go mod init github.com/libops/sitectl-myplugin
go mod edit -replace github.com/libops/sitectl=../..
go mod tidy
```

### Remote context not working

```bash
# Test SSH connection
ssh -p <port> <user>@<host>

# Check context config
sitectl config get-context <name>
```

---

## Contributing

Want to create a plugin? Great! Follow this guide and share it with the community.

Have improvements to the plugin system itself? Submit a PR to the [main sitectl repository](https://github.com/libops/sitectl).

---

## Resources

- [Plugin SDK Source](../pkg/plugin/sdk.go)
- [Docker Package](../pkg/docker/)
- [Config Package](../pkg/config/)
- [Example Plugins](../plugins/)
- [Cobra Documentation](https://github.com/spf13/cobra)
