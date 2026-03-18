# Contexts

`sitectl` organizes around a **site** and its **environments** into what's known as a **context**

- A **site** is the project itself. e.g. your Drupal site
- An **environment** is where that site runs: `local`, `staging`, `prod`, and so on.
- A **context** is the saved connection information for the given site environment.

Examples:

- `museum-local`: the museum site on your laptop
- `museum-prod`: the same site on a remote server

Contexts tell `sitectl` where Docker Compose lives and how to reach it.
