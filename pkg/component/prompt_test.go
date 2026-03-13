package component

import (
	"regexp"
	"strings"
	"testing"
)

func TestPromptStateUsesDefaultOnEmptyInput(t *testing.T) {
	t.Parallel()

	var prompt []string
	state, err := PromptState("fcrepo", StateGuidance{
		Question:     "Choose a state for fcrepo.",
		OnHelp:       "Use Fedora for binary storage.",
		OffHelp:      "Store files directly in Drupal.",
		DefaultState: StateOff,
	}, func(question ...string) (string, error) {
		for _, line := range question {
			prompt = append(prompt, stripANSI(line))
		}
		return "", nil
	})
	if err != nil {
		t.Fatalf("PromptState() error = %v", err)
	}
	if state != StateOff {
		t.Fatalf("expected default state %q, got %q", StateOff, state)
	}
	if len(prompt) < 6 {
		t.Fatalf("expected at least 6 prompt lines, got %d", len(prompt))
	}
	if prompt[0] != "FCREPO" {
		t.Fatalf("expected title line, got %q", prompt[0])
	}
	if prompt[1] != "" {
		t.Fatalf("expected blank line after title, got %q", prompt[1])
	}
	if !containsLine(prompt, "on  Use Fedora") {
		t.Fatalf("expected on help in prompt, got %#v", prompt)
	}
	if !containsLine(prompt, "off Store files") {
		t.Fatalf("expected off help in prompt, got %#v", prompt)
	}
	if !containsLine(prompt, "Choose fcrepo (on/off) [off]: ") {
		t.Fatalf("expected final prompt line, got %#v", prompt)
	}
}

func TestRenderSectionFormatsLikeHelpText(t *testing.T) {
	t.Parallel()

	rendered := stripANSI(RenderSection("This is Islandora", "Islandora is an open-source framework that provides the necessary tools to use a Drupal website as a fully-functional Digital Assets Management System."))
	lines := strings.Split(rendered, "\n")
	if lines[0] != "THIS IS ISLANDORA" {
		t.Fatalf("expected uppercase heading, got %q", lines[0])
	}
	if lines[1] != "" {
		t.Fatalf("expected blank line after heading, got %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "  Islandora is an open-source framework") {
		t.Fatalf("expected indented body, got %q", lines[2])
	}
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(value string) string {
	return ansiPattern.ReplaceAllString(value, "")
}

func containsLine(lines []string, fragment string) bool {
	for _, line := range lines {
		if strings.Contains(line, fragment) {
			return true
		}
	}
	return false
}

func TestPromptStateParsesInput(t *testing.T) {
	t.Parallel()

	state, err := PromptState("blazegraph", StateGuidance{}, func(question ...string) (string, error) {
		return "OFF", nil
	})
	if err != nil {
		t.Fatalf("PromptState() error = %v", err)
	}
	if state != StateOff {
		t.Fatalf("expected %q, got %q", StateOff, state)
	}
}

func TestPromptStateRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	_, err := PromptState("blazegraph", StateGuidance{}, func(question ...string) (string, error) {
		return "maybe", nil
	})
	if err == nil {
		t.Fatal("expected invalid input error")
	}
	if !strings.Contains(err.Error(), `invalid blazegraph value "maybe"`) {
		t.Fatalf("unexpected error %v", err)
	}
}
