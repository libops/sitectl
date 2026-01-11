# sitectl CLI

Command line utility to interact with your local and remote docker compose sites.

## Install

### Homebrew

You can install `sitectl` using homebrew

```bash
brew tap libops/homebrew
brew install libops/homebrew/sitectl
```

### Download Binary

Instead of homebrew, you can download a binary for your system from [the latest release of sitectl](https://github.com/libops/sitectl/releases/latest)

Then put the binary in a directory that is in your `$PATH`


## Why sitectl vs Docker Context?

While [Docker's native context feature](https://docs.docker.com/engine/manage-resources/contexts/) handles basic daemon connections, `sitectl` is purpose-built for Docker Compose projects and adds:

- **Enhanced remote operations**: SFTP file operations (read env files, upload/download), sudo support, and helpful SSH error messages
- **Container utilities**: Resolve service names to containers, extract secrets/env vars to better support `exec` operations inside containers, get container IPs within Docker networks
- **Plugin architecture**: Extend `sitectl` for project-specific needs (e.g. [islandora](https://github.com/libops/sitectl-isle), [drupal](https://github.com/libops/sitectl-drupal), etc.)
- **Service management**: Enable/disable services in docker-compose.yml with automatic cleanup of orphaned resources and Drupal configuration
- **Compose-first design**: Set the equivalent of `DOCKER_HOST`, `COMPOSE_PROJECT_NAME`, `COMPOSE_FILE`, `COMPOSE_ENV_FILES` automatically based on sitectl context settings
  - See [Docker's documentation](https://docs.docker.com/compose/how-tos/environment-variables/envvars/#configuration-details) for what these environment variables do


## Attribution

- The `config` commands for setting contexts were heavily inspired by `kubectl`
