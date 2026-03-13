package component

import (
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
		prompt = append(prompt, question...)
		return "", nil
	})
	if err != nil {
		t.Fatalf("PromptState() error = %v", err)
	}
	if state != StateOff {
		t.Fatalf("expected default state %q, got %q", StateOff, state)
	}
	if len(prompt) != 4 {
		t.Fatalf("expected 4 prompt lines, got %d", len(prompt))
	}
	if !strings.Contains(prompt[1], "on: Use Fedora") {
		t.Fatalf("expected on help in prompt, got %q", prompt[1])
	}
	if !strings.Contains(prompt[2], "off: Store files") {
		t.Fatalf("expected off help in prompt, got %q", prompt[2])
	}
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
