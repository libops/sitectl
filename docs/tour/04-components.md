# Components

Components describe optional parts of a stack in a structured way.

They are how `sitectl` marries infrastructure settings and app-specific settings into one reviewed configuration.

Instead of treating a stack as one large blob, `sitectl` lets each component carry its own defaults, follow-up questions, and operator guidance.

Common component states are:

- `enabled`: the component is part of the stack
- `disabled`: the component is not used
- `superseded`: another component replaces its role
- `distributed`: responsibility is moved out to external or split services

When you change a default component, you should expect operator implications such as:

- different infrastructure requirements
- different app-level wiring or environment values
- data movement or migration work
- different maintenance and failure modes

That is why component review matters: changing one default can affect both platform behavior and application behavior at the same time.
