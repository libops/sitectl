// Package plugin provides the SDK used by sitectl plugins.
//
// Host/plugin RPC uses typed JSON envelopes on the wire, but existing plugin
// commands still execute through Cobra. For plugin-owned commands, sitectl
// decodes RPC params into tagged structs, reconstructs the matching argv, and
// lets Cobra parse the flags and positionals again. This preserves existing
// BindFlags, hooks, help handling, and passthrough flag behavior while keeping
// the host/plugin wire format strict and testable.
//
// RPC param structs use these tags:
//   - rpc_flags:"name,alias" maps a field to one or more long flags.
//   - rpc_pos:"1" maps a string field to a positional argument.
//   - rpc_rootfs:"true" marks the rootfs path flag alias.
//   - rpc_methods:"method.name" limits a field to specific RPC methods.
//   - rpc_sensitive:"true" marks a field as unsafe for argv transport.
//
// Tagged RPC param fields currently support only bool and string. Positional
// params must be strings. Adding a tagged int, slice, map, or struct field is a
// registration-time contract error until the bridge explicitly supports that
// kind.
//
// RPC params are decoded strictly: unknown JSON fields and trailing JSON values
// are rejected. The host and plugin also use a lockstep RPC protocol version,
// so adding even an optional params field requires a coordinated sitectl and
// plugin rebuild rather than relying on JSON's usual additive compatibility.
//
// Registered plugin commands must declare every bridged flag. Registration
// validates that contract and panics during startup when a command is missing a
// required flag, so plugin authors see the mismatch before handling RPC calls.
// When adding a bridged flag or positional, update the params struct JSON and
// rpc_* tags, the matching Cobra flag or positional handling, the mirror
// comment that names the paired command, and the RPC extraction/round-trip
// tests in cmd/rpc_params_test.go and pkg/plugin/rpc_command_test.go.
//
// Current method-to-argv bridge map:
//
//	| Method | Params | Cobra argv contract |
//	| --- | --- | --- |
//	| plugin.metadata | none | no command argv |
//	| project.detect | ProjectDetectParams | JSON-only path |
//	| create.component_definitions | none | no command argv |
//	| create.run | CreateRunParams | create name plus Args |
//	| deploy.run | DeployRunParams | deploy hook plus Args |
//	| job.list | none | no command argv |
//	| job.run | JobRunParams | job name plus Args |
//	| component.list | ComponentListParams | list subcommand plus optional component name |
//	| component.describe | ComponentTargetParams | describe plus --component, --path, --codebase-rootfs/--drupal-rootfs, --verbose, --format, plus Args |
//	| component.reconcile | ComponentTargetParams | reconcile plus --component, --path, --codebase-rootfs/--drupal-rootfs, --report, --verbose, --format, --yolo, plus Args |
//	| component.set | ComponentSetParams | set plus name/disposition positionals, --path, --state, --disposition, --yolo, plus Args for non-secret follow-up flags |
//	| validate.run | ValidateRunParams | --codebase-rootfs/--drupal-rootfs plus Args |
//	| debug.run | DebugRunParams | --verbose plus Args |
//	| set.run | SetRunParams | --path plus Args |
//	| converge.run | ConvergeRunParams | --path, --codebase-rootfs/--drupal-rootfs, --report, --verbose, --format, plus Args |
//
// Component RPC is the most common bridged case. ComponentTargetParams.Name is
// tagged rpc_flags:"component", so the component describe command must declare
// --component. ComponentTargetParams.CodebaseRootfs is tagged
// rpc_flags:"codebase-rootfs,drupal-rootfs" and rpc_rootfs:"true", so the
// paired command must declare those aliases and mark the canonical rootfs flag.
// ComponentTargetParams.Report is limited to rpc_methods:"component.reconcile",
// so only the reconcile command declares --report. ComponentSetParams.Name and
// Disposition are tagged rpc_pos:"0" and rpc_pos:"1", so the component set
// command receives them as positionals before passthrough args.
//
// Command handlers must write through cmd.OutOrStdout() and cmd.ErrOrStderr().
// Direct process writes such as fmt.Println can corrupt the JSON RPC envelope.
// The host has a best-effort fallback that can recover a valid envelope from
// the last stdout line, but plugins must not depend on that fallback as a
// supported transport.
// RPC dispatch is also single-shot per plugin process; it mutates SDK config
// and Cobra command state while handling exactly one request.
package plugin
