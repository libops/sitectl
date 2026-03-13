package component

import (
	"fmt"

	"github.com/spf13/cobra"
)

type CreateOption struct {
	Name      string
	Default   State
	Guidance  StateGuidance
	Shorthand string
}

func (o CreateOption) normalizedDefault() State {
	if state := normalizeState(o.Default); state != "" {
		return state
	}
	if state := normalizeState(o.Guidance.DefaultState); state != "" {
		return state
	}
	return StateOn
}

func AddCreateFlags(cmd *cobra.Command, options ...CreateOption) {
	for _, option := range options {
		defaultState := option.normalizedDefault()
		usage := fmt.Sprintf("%s state: %s or %s", option.Name, StateOn, StateOff)
		if option.Shorthand != "" {
			cmd.Flags().StringP(option.Name, option.Shorthand, string(defaultState), usage)
			continue
		}
		cmd.Flags().String(option.Name, string(defaultState), usage)
	}
}

func ResolveCreateStates(cmd *cobra.Command, input InputFunc, options ...CreateOption) (map[string]State, error) {
	states := make(map[string]State, len(options))
	for _, option := range options {
		if option.Name == "" {
			return nil, fmt.Errorf("component create option name cannot be empty")
		}

		if cmd.Flags().Changed(option.Name) {
			value, err := cmd.Flags().GetString(option.Name)
			if err != nil {
				return nil, fmt.Errorf("get %s flag: %w", option.Name, err)
			}
			state, err := ParseState(value)
			if err != nil {
				return nil, fmt.Errorf("invalid %s value %q: %w", option.Name, value, err)
			}
			states[option.Name] = state
			continue
		}

		guidance := option.Guidance
		if guidance.DefaultState == "" {
			guidance.DefaultState = option.normalizedDefault()
		}
		state, err := PromptState(option.Name, guidance, input)
		if err != nil {
			return nil, err
		}
		states[option.Name] = state
	}

	return states, nil
}
