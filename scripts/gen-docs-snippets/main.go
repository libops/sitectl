// gen-docs-snippets generates MDX snippet files for each sitectl command.
// Run via: make docs-snippets
// Output goes to ../sitectl-docs/snippets/commands/
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sitectlcmd "github.com/libops/sitectl/cmd"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	displayPrefix = "sitectl"
	outputDir     = "../sitectl-docs/snippets/commands"
	autoGenHeader = "{/* Auto-generated from source. Run `make docs-snippets` to update. */}\n\n"
)

func main() {
	root := sitectlcmd.RootCmd
	root.DisableAutoGenTag = true

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create output dir: %v\n", err)
		os.Exit(1)
	}

	var count int
	walkCommands(root, func(cmd *cobra.Command) {
		slug := commandSlug(cmd)
		path := filepath.Join(outputDir, slug+".mdx")
		if err := os.WriteFile(path, []byte(renderSnippet(cmd)), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", path, err)
			os.Exit(1)
		}
		fmt.Println(path)
		count++
	})
	fmt.Printf("generated %d snippets\n", count)
}

func walkCommands(cmd *cobra.Command, fn func(*cobra.Command)) {
	for _, sub := range cmd.Commands() {
		if skipCommand(sub) {
			continue
		}
		fn(sub)
		walkCommands(sub, fn)
	}
}

func skipCommand(cmd *cobra.Command) bool {
	if cmd.Hidden {
		return true
	}
	name := cmd.Name()
	if name == "help" || name == "completion" {
		return true
	}
	// Skip thin plugin-passthrough commands (no Long, DisableFlagParsing, no subcommands)
	if cmd.DisableFlagParsing && strings.TrimSpace(cmd.Long) == "" && !cmd.HasAvailableSubCommands() {
		return true
	}
	return false
}

func commandSlug(cmd *cobra.Command) string {
	path := cmd.CommandPath()
	prefix := strings.ReplaceAll(displayPrefix, " ", "-")
	if strings.HasPrefix(path, displayPrefix+" ") {
		rel := path[len(displayPrefix)+1:]
		return strings.ToLower(prefix + "-" + strings.ReplaceAll(rel, " ", "-"))
	}
	return strings.ToLower(prefix)
}

func buildUseLine(cmd *cobra.Command) string {
	// cmd.CommandPath() already includes the correct display name (e.g. "sitectl compose")
	// for plugins it uses the CommandDisplayNameAnnotation ("sitectl isle create")
	path := cmd.CommandPath()

	var fullPath string
	if path == displayPrefix || strings.HasPrefix(path, displayPrefix+" ") {
		fullPath = path
	} else {
		fullPath = displayPrefix + " " + path
	}

	// Append args from Use (everything after the command name)
	useParts := strings.Fields(cmd.Use)
	if len(useParts) > 1 {
		fullPath += " " + strings.Join(useParts[1:], " ")
	}

	// For group commands (no RunE), append <command>
	if !cmd.Runnable() && cmd.HasAvailableSubCommands() {
		fullPath += " <command>"
	}

	return fullPath
}

func collectLocalFlags(cmd *cobra.Command) []*pflag.Flag {
	var flags []*pflag.Flag
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if !f.Hidden {
			flags = append(flags, f)
		}
	})
	return flags
}

func renderSnippet(cmd *cobra.Command) string {
	var b strings.Builder
	b.WriteString(autoGenHeader)

	// Long description, falling back to Short
	desc := strings.TrimSpace(cmd.Long)
	if desc == "" {
		desc = strings.TrimSpace(cmd.Short)
	}
	if desc != "" {
		b.WriteString(desc)
		b.WriteString("\n\n")
	}

	// Usage code block
	b.WriteString("```bash\n")
	b.WriteString(buildUseLine(cmd))
	b.WriteString("\n```\n")

	// Aliases
	if len(cmd.Aliases) > 0 {
		b.WriteString("\n**Aliases:** `")
		b.WriteString(strings.Join(cmd.Aliases, "`, `"))
		b.WriteString("`\n")
	}

	// Flags table (skip for DisableFlagParsing commands — they accept arbitrary args)
	if !cmd.DisableFlagParsing {
		flags := collectLocalFlags(cmd)
		if len(flags) > 0 {
			b.WriteString("\n| Flag | Default | Description |\n")
			b.WriteString("|------|---------|-------------|\n")
			for _, f := range flags {
				flagStr := "--" + f.Name
				if f.Shorthand != "" {
					flagStr = "-" + f.Shorthand + ", " + flagStr
				}
				defVal := f.DefValue
				if defVal == "" {
					defVal = " "
				} else {
					defVal = "`" + defVal + "`"
				}
				fmt.Fprintf(&b, "| `%s` | %s | %s |\n", flagStr, defVal, f.Usage)
			}
		}
	}

	return b.String()
}
