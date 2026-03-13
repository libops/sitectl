package component

import (
	"fmt"
	"strings"

	"github.com/libops/sitectl/pkg/config"
)

type InputFunc func(question ...string) (string, error)

type StateGuidance struct {
	Question     string
	OnHelp       string
	OffHelp      string
	DefaultState State
}

func PromptState(name string, guidance StateGuidance, input InputFunc) (State, error) {
	if input == nil {
		input = config.GetInput
	}

	defaultState := normalizeState(guidance.DefaultState)
	if defaultState == "" {
		defaultState = StateOn
	}

	lines := []string{}
	if guidance.Question != "" {
		lines = append(lines, guidance.Question)
	}
	if guidance.OnHelp != "" {
		lines = append(lines, fmt.Sprintf("  on: %s", guidance.OnHelp))
	}
	if guidance.OffHelp != "" {
		lines = append(lines, fmt.Sprintf("  off: %s", guidance.OffHelp))
	}
	lines = append(lines, fmt.Sprintf("Set %s to %q or %q [%s]: ", name, StateOn, StateOff, defaultState))

	value, err := input(lines...)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" {
		return defaultState, nil
	}

	state, err := ParseState(value)
	if err != nil {
		return "", fmt.Errorf("invalid %s value %q: %w", name, value, err)
	}
	return state, nil
}
