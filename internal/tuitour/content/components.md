# Components

Components describe optional parts of a stack in a structured way.

They are how `sitectl` marries infrastructure settings and app-specific settings into one reviewed configuration.

Instead of treating a stack as one large blob, `sitectl` lets each component carry its own defaults, follow-up questions, and operator guidance.

Common component states are:

- `enabled`: the component is part of the stack
- `disabled`: the component is not used
- `superseded`: another component replaces its role
- `distributed`: responsibility is moved out to external or split services
