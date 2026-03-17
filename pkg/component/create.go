package component

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

type CreateOption struct {
	Name                string
	Default             State
	DefaultDisposition  Disposition
	AllowedDispositions []Disposition
	Guidance            StateGuidance
	Shorthand           string
	PromptOnCreate      bool
	FollowUps           []FollowUpSpec
}

func (o CreateOption) normalizedDefault() State {
	if disposition := o.normalizedDispositionDefault(); disposition != "" {
		return DispositionToState(disposition)
	}
	if state := normalizeState(o.Default); state != "" {
		return state
	}
	if state := normalizeState(o.Guidance.DefaultState); state != "" {
		return state
	}
	return StateOn
}

func (o CreateOption) normalizedDispositionDefault() Disposition {
	if disposition := normalizeDisposition(o.DefaultDisposition); disposition != "" {
		return disposition
	}
	if disposition := normalizeDisposition(StateToDisposition(o.Default)); disposition != "" {
		return disposition
	}
	if disposition := normalizeDisposition(StateToDisposition(o.Guidance.DefaultState)); disposition != "" {
		return disposition
	}
	return DispositionEnabled
}

func (o CreateOption) shouldPromptOnCreate() bool {
	return o.PromptOnCreate
}

func AddCreateFlags(cmd *cobra.Command, options ...CreateOption) {
	seenFollowUpFlags := map[string]bool{}
	for _, option := range options {
		defaultDisposition := option.normalizedDispositionDefault()
		usage := fmt.Sprintf("%s disposition", option.Name)
		if len(option.AllowedDispositions) > 0 {
			allowed := []string{}
			for _, disposition := range option.AllowedDispositions {
				allowed = append(allowed, string(disposition))
			}
			usage = fmt.Sprintf("%s disposition: %s", option.Name, strings.Join(allowed, ", "))
		}
		if option.Shorthand != "" {
			cmd.Flags().StringP(option.Name, option.Shorthand, string(defaultDisposition), usage)
		} else {
			cmd.Flags().String(option.Name, string(defaultDisposition), usage)
		}
		for _, followUp := range option.FollowUps {
			if !followUp.PromptOnCreate {
				continue
			}
			flagName := followUpFlagName(option.Name, followUp)
			if flagName == "" || seenFollowUpFlags[flagName] || cmd.Flags().Lookup(flagName) != nil {
				continue
			}
			seenFollowUpFlags[flagName] = true
			cmd.Flags().String(flagName, strings.TrimSpace(followUp.DefaultValue), createFollowUpUsage(option.Name, followUp))
		}
	}
}

func ResolveCreateStates(cmd *cobra.Command, input InputFunc, options ...CreateOption) (map[string]State, error) {
	decisions, err := ResolveCreateDecisions(cmd, input, options...)
	if err != nil {
		return nil, err
	}
	states := make(map[string]State, len(options))
	for name, decision := range decisions {
		states[name] = decision.State
	}
	return states, nil
}

func ResolveCreateDecisions(cmd *cobra.Command, input InputFunc, options ...CreateOption) (map[string]ReviewDecision, error) {
	decisions := make(map[string]ReviewDecision, len(options))
	for _, option := range options {
		if option.Name == "" {
			return nil, fmt.Errorf("component create option name cannot be empty")
		}

		var state State
		var disposition Disposition
		if cmd.Flags().Changed(option.Name) {
			value, err := cmd.Flags().GetString(option.Name)
			if err != nil {
				return nil, fmt.Errorf("get %s flag: %w", option.Name, err)
			}
			disposition, err = ParseDisposition(value)
			if err != nil {
				return nil, fmt.Errorf("invalid %s value %q: %w", option.Name, value, err)
			}
			disposition, err = ResolveAllowedDisposition(option.AllowedDispositions, disposition)
			if err != nil {
				return nil, fmt.Errorf("invalid %s value %q: %w", option.Name, value, err)
			}
			state = DispositionToState(disposition)
		} else if !option.shouldPromptOnCreate() {
			disposition = option.normalizedDispositionDefault()
			state = DispositionToState(disposition)
		} else {
			var err error
			if len(option.AllowedDispositions) > 0 {
				disposition, err = PromptDisposition(option.Name, option.Guidance, option.AllowedDispositions, option.normalizedDispositionDefault(), input)
				if err != nil {
					return nil, err
				}
				state = DispositionToState(disposition)
			} else {
				guidance := option.Guidance
				if guidance.DefaultState == "" {
					guidance.DefaultState = option.normalizedDefault()
				}
				state, err = PromptState(option.Name, guidance, input)
				if err != nil {
					return nil, err
				}
				disposition = StateToDisposition(state)
			}
		}

		decision := ReviewDecision{
			Disposition: disposition,
			State:       state,
			Options:     map[string]string{},
		}
		if err := PromptCreateFollowUps(cmd, option, &decision, input); err != nil {
			return nil, err
		}
		decisions[option.Name] = decision
	}

	return decisions, nil
}

func PromptCreateFollowUps(cmd *cobra.Command, option CreateOption, decision *ReviewDecision, input InputFunc) error {
	if decision == nil {
		return fmt.Errorf("create decision cannot be nil")
	}
	if decision.Options == nil {
		decision.Options = map[string]string{}
	}

	for _, followUp := range followUpsForDisposition(option.FollowUps, decision.Disposition, decision.State) {
		flagName := followUpFlagName(option.Name, followUp)
		if flagName != "" && cmd != nil && cmd.Flags().Lookup(flagName) != nil && cmd.Flags().Changed(flagName) {
			value, err := cmd.Flags().GetString(flagName)
			if err != nil {
				return fmt.Errorf("get %s flag: %w", flagName, err)
			}
			decision.Options[followUp.Name] = strings.TrimSpace(value)
			continue
		}
		if !followUp.PromptOnCreate {
			if defaultValue := strings.TrimSpace(followUp.DefaultValue); defaultValue != "" {
				decision.Options[followUp.Name] = defaultValue
			}
			continue
		}

		value, err := PromptFollowUp(option.Name, followUp, strings.TrimSpace(followUp.DefaultValue), input, nil)
		if err != nil {
			return err
		}
		decision.Options[followUp.Name] = strings.TrimSpace(value)
	}
	return nil
}
