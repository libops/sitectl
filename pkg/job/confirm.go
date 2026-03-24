package job

import (
	"fmt"
	"strings"

	"github.com/libops/sitectl/pkg/config"
)

// ConfirmDatabaseReplacement prompts the user to confirm a destructive
// database import. It returns true when the user confirms, false when they
// decline, and an error if reading input fails.
//
// yolo skips the prompt and returns true immediately, intended for
// non-interactive pipelines where the caller has already passed a --yolo flag.
func ConfirmDatabaseReplacement(targetContext, databaseName, inputPath string, yolo bool) (bool, error) {
	if yolo {
		return true, nil
	}

	prompt := []string{
		fmt.Sprintf("About to import %s database artifact %q into context %q.", databaseName, inputPath, targetContext),
		"This will wipe out the target database.",
		"Continue? [y/N]: ",
	}

	input, err := config.GetInput(prompt...)
	if err != nil {
		return false, err
	}

	switch strings.ToLower(strings.TrimSpace(input)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}
