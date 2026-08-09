# sitectl SDK migration notes

## Direct Git sync APIs

The pre-customer v1 SDK removed the Git shell-program builders. They could not
preserve dirty-worktree checks, remote selection, exact-ref verification, and
safe local/remote execution without asking callers to evaluate a shell string.

| Removed API | Replacement |
| --- | --- |
| `(config.Context).GitSyncShellCommand(branch string) string` | `(*config.Context).SyncGitCheckout(ctx, out, branch) error` |
| `config.GitSyncShellCommand(branch string) string` | Obtain a `*config.Context`, then call `SyncGitCheckout` |
| `(config.Context).GitSyncRefShellCommand(ref string) string` | `(*config.Context).SyncGitRefCheckout(ctx, out, ref) error` |
| `config.GitSyncRefShellCommand(ref string) string` | Obtain a `*config.Context`, then call `SyncGitRefCheckout` |

Call the replacement directly; do not pass its work through `bash -c`,
`sh -c`, or lifecycle command metadata. Both methods execute ordered Git argv
through the context, so the same call supports local and remote projects.

```go
if err := siteContext.SyncGitCheckout(cmd.Context(), cmd.OutOrStdout(), branch); err != nil {
	return err
}
if err := siteContext.SyncGitRefCheckout(cmd.Context(), cmd.OutOrStdout(), ref); err != nil {
	return err
}
```

## Dynamic Compose-project argv

The pre-customer v1 SDK removed the public string command and shell-builder
helpers. Quoting dynamic argv into a string and then parsing it as trusted
lifecycle metadata corrupted literal dollar signs and rejected line breaks in
valid customer content.

| Removed API | Replacement |
| --- | --- |
| `(*plugin.SDK).RunActiveComposeProjectCommand(cmd, command string)` | `RunActiveComposeProjectArgv(cmd, argv []string)` |
| `plugin.DockerComposeExecCommand(service string, args ...string) string` | `plugin.DockerComposeExecArgv(service, args...) []string` |
| `plugin.ShellJoin(args []string) string` | Pass the original `[]string` directly |
| `plugin.ShellQuote(value string) string` | Pass the original string as one argv entry |

For a normal Compose exec, keep each user value as one argument:

```go
argv := plugin.DockerComposeExecArgv("wordpress", append([]string{"wp"}, args...)...)
return sdk.RunActiveComposeProjectArgv(cmd, argv)
```

Containerized tools that need the local or remote host identity and checkout
path must not pass literal `$(id -u)`, `$(id -g)`, or `$PWD`. Build their argv
from resolved values instead:

```go
return sdk.RunActiveComposeProjectHostArgv(cmd, func(host plugin.ComposeProjectHost) []string {
	argv := []string{
		"docker", "compose", "run", "--rm", "--no-deps",
	}
	if host.HasNumericIdentity {
		argv = append(argv, "--user", host.UID+":"+host.GID)
	}
	return append(argv,
		"--volume", host.ProjectDir+":/workspace:z",
		"--workdir", "/workspace", "--entrypoint", "composer", "wordpress",
	)
})
```

`host.ProjectDir` is the path visible to the Docker host, which can differ from
the CLI checkout path for an sshfs-mounted local workspace. Native Windows
hosts set `HasNumericIdentity` to false and leave `UID`/`GID` empty; omit
POSIX-only ownership arguments in that case. Remote POSIX hosts still provide
numeric identity even when the sitectl client runs on Windows.

Append dynamic tool arguments to that returned slice. Keep application-owned
build/init/up/down/rollout strings in the lifecycle-list APIs; those remain
constrained, static metadata and require checked-in interpreter programs.

When one operator action needs several dynamic commands to remain atomic, pass
the whole sequence to `RunActiveComposeProjectArgvList`. It validates every argv
before executing the first command and holds one project mutation lock across
the ordered list. For example, a database export can create a destination,
export inside the application container, and copy the result without another
deploy, restore, or reconcile interleaving between those steps:

```go
return sdk.RunActiveComposeProjectArgvList(cmd, [][]string{
	{"mkdir", "-p", backupDir},
	plugin.DockerComposeExecArgv("wordpress", "wp", "db", "export", containerPath),
	{"docker", "compose", "cp", "wordpress:" + containerPath, hostPath},
})
```
