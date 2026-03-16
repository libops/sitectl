package component

import (
	"fmt"
	"strings"

	"github.com/libops/sitectl/pkg/config"
)

type FollowUpValue struct {
	Value string
}

func followUpsForDisposition(specs []FollowUpSpec, disposition Disposition, state State) []FollowUpSpec {
	if len(specs) == 0 {
		return nil
	}
	out := make([]FollowUpSpec, 0, len(specs))
	for _, spec := range specs {
		if spec.Name == "" {
			continue
		}
		if spec.AppliesToDisposition != "" && normalizeDisposition(spec.AppliesToDisposition) != normalizeDisposition(disposition) {
			continue
		}
		if spec.AppliesTo != "" && normalizeState(spec.AppliesTo) != normalizeState(state) {
			continue
		}
		out = append(out, spec)
	}
	return out
}

func followUpFlagName(componentName string, spec FollowUpSpec) string {
	if strings.TrimSpace(spec.FlagName) != "" {
		return strings.TrimSpace(spec.FlagName)
	}
	if componentName == "" || spec.Name == "" {
		return ""
	}
	return componentName + "-" + spec.Name
}

func createFollowUpUsage(componentName string, spec FollowUpSpec) string {
	if strings.TrimSpace(spec.FlagUsage) != "" {
		return strings.TrimSpace(spec.FlagUsage)
	}
	label := strings.TrimSpace(spec.Label)
	if label == "" {
		label = strings.TrimSpace(spec.Name)
	}
	if componentName == "" {
		return label
	}
	return fmt.Sprintf("%s for %s", label, componentName)
}

func PromptFollowUp(componentName string, spec FollowUpSpec, defaultValue string, input InputFunc, promptChoice PromptChoiceFunc) (string, error) {
	if input == nil {
		input = config.GetInput
	}
	if promptChoice == nil {
		promptChoice = PromptChoice
	}
	if len(spec.Choices) == 0 {
		lines := []string{}
		if section := RenderFollowUpSection(componentName, spec); section != "" {
			lines = append(lines, strings.Split(section, "\n")...)
		}
		prompt := strings.TrimSpace(spec.CustomPrompt)
		if prompt == "" {
			label := strings.TrimSpace(spec.Label)
			if label == "" {
				label = spec.Name
			}
			prompt = label + ": "
		}
		lines = append(lines, "", RenderPromptLine(prompt))
		value, err := input(lines...)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(value) == "" {
			return strings.TrimSpace(defaultValue), nil
		}
		return strings.TrimSpace(value), nil
	}

	promptName := followUpFlagName(componentName, spec)
	if promptName == "" {
		promptName = spec.Name
	}
	value, err := promptChoice(promptName, spec.Choices, defaultValue, input, strings.Split(RenderFollowUpSection(componentName, spec), "\n")...)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(spec.CustomPrompt) != "" && isCustomChoice(spec, value) {
		lines := []string{}
		if section := RenderFollowUpSection(componentName, spec); section != "" {
			lines = append(lines, strings.Split(section, "\n")...)
		}
		lines = append(lines, "", RenderPromptLine(spec.CustomPrompt))
		customValue, err := input(lines...)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(customValue), nil
	}
	return strings.TrimSpace(value), nil
}

func RenderFollowUpSection(componentName string, spec FollowUpSpec) string {
	title := strings.TrimSpace(spec.Label)
	if title == "" {
		title = strings.TrimSpace(spec.Name)
	}
	if componentName != "" && title != "" {
		title = componentName + ": " + title
	}
	return RenderSection(title, strings.TrimSpace(spec.Question))
}

func isCustomChoice(spec FollowUpSpec, value string) bool {
	for _, choice := range spec.Choices {
		if choice.Value != value {
			continue
		}
		return choice.AllowCustomInput
	}
	return false
}

func RenderConfiguredFollowUps(view ReviewView) []string {
	if len(view.Definition.FollowUps) == 0 || len(view.FollowUpValues) == 0 {
		return nil
	}
	lines := []string{}
	for _, spec := range view.Definition.FollowUps {
		value := strings.TrimSpace(view.FollowUpValues[spec.Name])
		if value == "" {
			continue
		}
		if view.State != StateDrifted && spec.AppliesToDisposition != "" && normalizeDisposition(spec.AppliesToDisposition) != normalizeDisposition(view.Disposition) {
			continue
		}
		if view.State != StateDrifted && spec.AppliesToDisposition == "" && spec.AppliesTo != "" && normalizeState(spec.AppliesTo) != normalizeState(State(view.State)) {
			continue
		}
		label := strings.TrimSpace(spec.Label)
		if label == "" {
			label = spec.Name
		}
		lines = append(lines, fmt.Sprintf("%s: %s", label, value))
	}
	return lines
}

func PromptDeclaredReviewFollowUps(view ReviewView, decision *ReviewDecision, input InputFunc, promptChoice PromptChoiceFunc) error {
	if decision == nil {
		return fmt.Errorf("review decision cannot be nil")
	}
	if decision.Options == nil {
		decision.Options = map[string]string{}
	}
	for _, spec := range view.Definition.FollowUpsForDisposition(decision.Disposition) {
		defaultValue := strings.TrimSpace(view.FollowUpValues[spec.Name])
		if defaultValue == "" {
			defaultValue = strings.TrimSpace(spec.DefaultValue)
		}
		value, err := PromptFollowUp(view.Name, spec, defaultValue, input, promptChoice)
		if err != nil {
			return err
		}
		decision.Options[spec.Name] = strings.TrimSpace(value)
	}
	return nil
}

func RenderDecisionFollowUps(def Definition, decision ReviewDecision) string {
	parts := []string{}
	for _, spec := range def.FollowUpsForDisposition(decision.Disposition) {
		value := strings.TrimSpace(decision.Options[spec.Name])
		if value == "" {
			continue
		}
		label := strings.TrimSpace(spec.Label)
		if label == "" {
			label = spec.Name
		}
		parts = append(parts, fmt.Sprintf("%s: `%s`.", label, value))
	}
	return strings.Join(parts, " ")
}

func buildReportFollowUps(view ReviewView) map[string]string {
	if len(view.FollowUpValues) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, spec := range view.Definition.FollowUps {
		value := strings.TrimSpace(view.FollowUpValues[spec.Name])
		if value == "" {
			continue
		}
		if view.State != StateDrifted && spec.AppliesToDisposition != "" && normalizeDisposition(spec.AppliesToDisposition) != normalizeDisposition(view.Disposition) {
			continue
		}
		if view.State != StateDrifted && spec.AppliesToDisposition == "" && spec.AppliesTo != "" && normalizeState(spec.AppliesTo) != normalizeState(State(view.State)) {
			continue
		}
		out[spec.Name] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
