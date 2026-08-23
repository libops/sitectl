//go:build linux

package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestHostCommandIsPublicProvisioningSurface(t *testing.T) {
	command := hostCommand()
	if command.Hidden {
		t.Fatal("host command is hidden")
	}
	if command.GroupID != "advanced" {
		t.Fatalf("host command group = %q, want advanced", command.GroupID)
	}
	if !strings.Contains(command.Long, "target host") {
		t.Fatalf("host command help does not explain its execution target: %q", command.Long)
	}
	assertCommandHelp(t, command)
}

func assertCommandHelp(t *testing.T, command *cobra.Command) {
	t.Helper()
	for _, child := range command.Commands() {
		if strings.TrimSpace(child.Short) == "" {
			t.Errorf("host command %q has no short description", child.CommandPath())
		}
		assertCommandHelp(t, child)
	}
}
