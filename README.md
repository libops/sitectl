# sitectl CLI

Command line utility to interact with your local and remote docker compose sites.

## Install

### Homebrew

You can install `sitectl` using homebrew

```bash
brew tap libops/homebrew https://github.com/libops/homebrew
brew install libops/homebrew/sitectl
```

### Download Binary

Instead of homebrew, you can download a binary for your system from [the latest release of sitectl](https://github.com/libops/sitectl/releases/latest)

Then put the binary in a directory that is in your `$PATH`

## Usage

```bash
$ sitectl --help
Interact with your docker compose site

Usage:
  sitectl [command]

Available Commands:
  completion   Generate the autocompletion script for the specified shell
  compose      Run docker compose commands on sitectl contexts
  config       Manage sitectl command configuration
  help         Help about any command
  make         Run custom make commands
  port-forward Forward one or more local ports to a service
  sequelace    Connect to your MySQL/Mariadb database using Sequel Ace (Mac OS only)

Flags:
      --context string     The sitectl context to use. See sitectl config --help for more info (default "local")
  -h, --help               help for sitectl
      --log-level string   The logging level for the command (default "DEBUG")

Use "sitectl [command] --help" for more information about a command.
```

## Why sitectl vs Docker Context?

While [Docker's native context feature](https://docs.docker.com/engine/manage-resources/contexts/) handles basic daemon connections, `sitectl` is purpose-built for Docker Compose projects and adds:

- **Enhanced remote operations**: SFTP file operations (read env files, upload/download), sudo support, and helpful SSH error messages
- **Container utilities**: Resolve service names to containers, extract secrets/env vars to better support `exec` operations inside containers, get container IPs within Docker networks
- **Service management**: Enable/disable services in docker-compose.yml with automatic cleanup of orphaned resources and Drupal configuration
- **Compose-first design**: Set the equivalent of `DOCKER_HOST`, `COMPOSE_PROJECT_NAME`, `COMPOSE_FILE`, `COMPOSE_ENV_FILES` automatically based on sitectl context settings
  - See [Docker's documentation](https://docs.docker.com/compose/how-tos/environment-variables/envvars/#configuration-details) for what these environment variables do

## Plugins

`sitectl` can be extended for project-specific needs

- [islandora](https://github.com/libops/sitectl-isle)
- [drupal](https://github.com/libops/sitectl-drupal)


## Attribution

- The `config` commands for setting contexts were heavily inspired by `kubectl`
