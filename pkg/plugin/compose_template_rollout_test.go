package plugin

import (
	"strings"
	"testing"
)

func TestDefaultComposeRolloutCommandsFailWhenPullFails(t *testing.T) {
	commands := DefaultComposeRolloutCommands()
	if len(commands) == 0 {
		t.Fatal("DefaultComposeRolloutCommands() returned no commands")
	}
	if !strings.Contains(commands[0], "|| docker compose pull --ignore-buildable") {
		t.Fatalf("pull command %q no longer provides the compatible pull fallback", commands[0])
	}
	if strings.Contains(commands[0], "|| true") {
		t.Fatalf("pull command %q suppresses an unrecoverable pull failure", commands[0])
	}
}
