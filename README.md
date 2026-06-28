# sitectl

`sitectl` is the LibOps command-line tool for managing Docker Compose-backed application sites. It keeps contexts, Compose lifecycle commands, service helpers, component changes, local image overrides, validation, health checks, and deployment entrypoints in one consistent CLI.

Documentation: https://sitectl.libops.io

## Requirements

- Docker with the Compose v2 plugin for local application contexts.
- Go 1.26.1 when building `sitectl` from source.
- Application plugins such as [`sitectl-drupal`](https://github.com/libops/sitectl-drupal), [`sitectl-isle`](https://github.com/libops/sitectl-isle), [`sitectl-wp`](https://github.com/libops/sitectl-wp), [`sitectl-ojs`](https://github.com/libops/sitectl-ojs), [`sitectl-omeka-classic`](https://github.com/libops/sitectl-omeka-classic), [`sitectl-omeka-s`](https://github.com/libops/sitectl-omeka-s), or [`sitectl-archivesspace`](https://github.com/libops/sitectl-archivesspace) for app-specific create flows and helpers.

## Quick Start

Install `sitectl`, install the app plugin for the template you want to run, then create a local site:

```bash
sitectl create wp/default \
  --template-repo https://github.com/libops/wp \
  --path ./my-wordpress-site \
  --type local \
  --checkout-source template \
  --default-context
```

Start the site with the context-aware Compose wrapper:

```bash
sitectl compose up --remove-orphans -d
```

See [Managed applications](https://sitectl.libops.io/apps) for the Compose, local image build, HTTPS, and development override contract.

## Common Operations

Compose lifecycle is documented in [`sitectl compose`](https://sitectl.libops.io/commands/compose):

```bash
sitectl compose ps
sitectl compose logs -f
sitectl compose down
```

Context and site checks are documented in [`sitectl healthcheck`](https://sitectl.libops.io/commands/healthcheck) and [`sitectl validate`](https://sitectl.libops.io/commands/validate):

```bash
sitectl healthcheck
sitectl validate
```

Local image overrides are documented in [`sitectl image`](https://sitectl.libops.io/commands/image):

```bash
sitectl image set --image app=ghcr.io/example/app:pr-123
```

Component changes are written with [`sitectl set`](https://sitectl.libops.io/commands/set) and applied with [`sitectl converge`](https://sitectl.libops.io/commands/converge):

```bash
sitectl set upload-limits enabled --max-upload-size 2G --upload-timeout 10m
sitectl converge
```

## License

`sitectl` is licensed under the MIT License.

## Attribution

- The `config` commands for setting contexts were heavily inspired by `kubectl`.
- Adding a TUI was inspired by 37signals' [once](https://github.com/basecamp/once) CLI.
