# Context

`sitectl` organizes around a **site** and its **environments** into what it calls a context.

- A **site** is the project itself.
- An **environment** is where that site runs: `local`, `staging`, `prod`, and so on.
- A **context** is the saved connection information for the given site environment.

Examples:

- `museum-local`
- `museum-prod`

Contexts tell `sitectl` where Docker Compose lives and how to reach it.
