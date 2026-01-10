# sitectl CLI

Command line utility to interact with your local and remote docker compose sites.

## Why sitectl vs Docker Context?

While [Docker's native context feature](https://docs.docker.com/engine/manage-resources/contexts/) handles basic daemon connections, `sitectl` is purpose-built for Docker Compose projects and adds:

- **Enhanced remote operations**: SFTP file operations (read env files, upload/download), sudo support, and helpful SSH error messages
- **Container utilities**: Resolve service names to containers, extract secrets/env vars to better support `exec` operations inside containers, get container IPs within Docker networks
- **Plugin architecture**: Extend `sitectl` for project-specific needs (e.g. islandora, drupal, etc.)
- **Service management**: Enable/disable services in docker-compose.yml with automatic cleanup of orphaned resources and Drupal configuration
- **Compose-first design**: Set the equivalent of `COMPOSE_PROJECT_NAME`, `COMPOSE_FILE`, `COMPOSE_ENV_FILES`, automatically based on sitectl context settings
  - See [Docker's documentation](https://docs.docker.com/compose/how-tos/environment-variables/envvars/#configuration-details) for what these environment variables do


## Attribution

- The `config` commands for setting contexts were heavily inspired by `kubectl`
