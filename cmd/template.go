package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
)

type templateProvenance struct {
	Kind     string                              `yaml:"kind"`
	Template struct{ Repository, Commit string } `yaml:"template"`
}

var lockedTemplateCommitPattern = regexp.MustCompile(`^[a-fA-F0-9]{40}([a-fA-F0-9]{24})?$`)

func init() { RootCmd.AddCommand(templateCommand()) }

func templateCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "template", Short: "Inspect the upstream template recorded for this site", GroupID: "ops"}
	cmd.AddCommand(templateDiffCommand())
	return cmd
}

func templateDiffCommand() *cobra.Command {
	return &cobra.Command{Use: "diff", Short: "Report files changed upstream and downstream since this site was created", Args: cobra.NoArgs,
		Long: "Read .libops/template.lock.yaml, fetch the recorded repository into a temporary checkout, and show separate upstream and downstream name-status diffs. The site checkout is not modified.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := resolveCurrentContext(cmd)
			if err != nil {
				return err
			}
			lockPath := filepath.Join(ctx.ProjectDir, ".libops", "template.lock.yaml")
			data, err := ctx.ReadFile(lockPath)
			if err != nil {
				return fmt.Errorf("read template provenance: %w", err)
			}
			var lock templateProvenance
			if err := yaml.Unmarshal(data, &lock); err != nil {
				return fmt.Errorf("parse template provenance: %w", err)
			}
			if lock.Kind != "TemplateLock" || strings.TrimSpace(lock.Template.Repository) == "" || strings.TrimSpace(lock.Template.Commit) == "" {
				return fmt.Errorf("template lock does not contain a repository and commit")
			}
			if strings.HasPrefix(lock.Template.Repository, "-") || !lockedTemplateCommitPattern.MatchString(lock.Template.Commit) {
				return fmt.Errorf("template lock contains an unsafe repository or invalid commit")
			}
			temp, err := os.MkdirTemp("", "sitectl-template-diff-*")
			if err != nil {
				return err
			}
			defer os.RemoveAll(temp)
			clone := exec.CommandContext(cmd.Context(), "git", "clone", "--quiet", "--no-tags", "--", lock.Template.Repository, temp) // #nosec G204 -- repository is a validated positional argument from explicit site provenance.
			clone.Stderr = cmd.ErrOrStderr()
			if err := clone.Run(); err != nil {
				return fmt.Errorf("fetch template: %w", err)
			}
			if output, err := exec.CommandContext(cmd.Context(), "git", "-C", temp, "cat-file", "-e", lock.Template.Commit+"^{commit}").CombinedOutput(); err != nil { // #nosec G204 -- commit is restricted to a full hexadecimal object ID.
				return fmt.Errorf("recorded template commit is unavailable: %w: %s", err, strings.TrimSpace(string(output)))
			}
			upstream, err := exec.CommandContext(cmd.Context(), "git", "-C", temp, "diff", "--name-status", lock.Template.Commit+"..HEAD").Output() // #nosec G204 -- commit is restricted to a full hexadecimal object ID.
			if err != nil {
				return fmt.Errorf("compare upstream template: %w", err)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Upstream changes since template lock:")
			if strings.TrimSpace(string(upstream)) == "" {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  none")
			} else {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(string(upstream)))
			}
			if ctx.DockerHostType != "local" {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Downstream working-tree diff is unavailable for remote contexts.")
				return nil
			}
			downstream := exec.CommandContext(cmd.Context(), "git", "--no-pager", "diff", "--name-status", lock.Template.Commit) // #nosec G204 -- commit is restricted to a full hexadecimal object ID.
			downstream.Dir = ctx.ProjectDir
			output, err := downstream.CombinedOutput()
			if err != nil {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Downstream comparison requires the recorded commit in local Git history.")
				return nil
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Downstream changes since template lock:")
			if strings.TrimSpace(string(output)) == "" {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  none")
			} else {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(string(output)))
			}
			return nil
		},
	}
}
