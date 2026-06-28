package cmd

import (
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
)

func TestConfirmComposeCleanRequiresDeleteToken(t *testing.T) {
	oldInput := composeCleanInput
	t.Cleanup(func() { composeCleanInput = oldInput })

	var prompt string
	composeCleanInput = func(question ...string) (string, error) {
		prompt = strings.Join(question, "\n")
		return "yes", nil
	}
	err := confirmComposeClean(&config.Context{Name: "wp", ProjectDir: "/tmp/wp"}, false)
	if err == nil {
		t.Fatal("expected loose confirmation to be rejected")
	}
	if !strings.Contains(prompt, "delete wp") || !strings.Contains(prompt, "permanently delete") {
		t.Fatalf("prompt did not clearly require delete token:\n%s", prompt)
	}

	composeCleanInput = func(question ...string) (string, error) {
		return "delete wp", nil
	}
	if err := confirmComposeClean(&config.Context{Name: "wp", ProjectDir: "/tmp/wp"}, false); err != nil {
		t.Fatalf("confirmComposeClean() error = %v", err)
	}
}
