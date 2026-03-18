# Contributing

## UI Architecture

`sitectl` supports two interaction modes:

- one-off command execution such as `sitectl compose ps`
- an embedded TUI dashboard launched by running `sitectl` with no additional arguments

Because both modes need to share behavior, interactive command UIs must be designed as composable Bubble Tea models instead of bespoke terminal flows.

### Rule

When a command needs interactive UI:

- keep business logic separate from UI state and rendering
- make the UI self-contained inside the command or shared UI package
- ensure the same UI can run standalone or be embedded inside the dashboard

In practice, command implementations should follow this split:

- service layer: pure command logic and side effects
- UI layer: Bubble Tea model and Bubbles-based components
- Cobra layer: chooses between non-interactive execution and launching the UI

### Required Libraries

Interactive `sitectl` UIs should build on the shared TUI stack already in use:

- `bubbletea` for state, events, and screen management
- `bubbles` for list, help, input, viewport, progress, and similar primitives
- `lipgloss` for styling and layout
- `bubblezone` for click targets and mouse hit detection where needed
- `harmonica` for motion and transitions where appropriate
- `ntcharts` for terminal charts where appropriate

### What Not To Do

Do not implement custom terminal widgets when the library stack already provides them.

Examples:

- do not hand-roll a select menu when `bubbles/list` fits
- do not hand-roll a text input when `bubbles/textinput` or `textarea` fits
- do not hand-roll help footers when `bubbles/help` fits
- do not hand-roll scroll containers when `bubbles/viewport` fits

`lipgloss` should be used for presentation and composition, not as a replacement for Bubble Tea/Bubbles interaction primitives.

### Shared Components

Reusable interaction primitives should live in shared UI packages so commands and the dashboard can both consume them.

Current direction:

- shared prompt/select/input components belong in `pkg/ui`
- command-specific interactive screens can live near the command, but should still be Bubble Tea models
- older bespoke prompt implementations should be migrated to shared Bubble Tea/Bubbles components over time

### Design Goal

A command that has an interactive flow should be embeddable in the dashboard without rewriting its UI logic.

That means a command UI should be structured so it can be:

- launched directly from Cobra
- pushed or mounted inside the dashboard TUI

If a proposed command UI cannot be reused that way, it should be redesigned before being added.
