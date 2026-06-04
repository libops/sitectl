package component

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// WriteComponentCatalog renders component names, dispositions, and set-time flag
// guidance for a plugin component registry.
func WriteComponentCatalog(out io.Writer, pluginName string, defs []Definition, componentName string) error {
	defs = filterCatalogDefinitions(defs, componentName)
	if len(defs) == 0 {
		if strings.TrimSpace(componentName) != "" {
			return fmt.Errorf("unknown component %q", componentName)
		}
		_, err := fmt.Fprintln(out, "No components are registered.")
		return err
	}

	title := "Components"
	if strings.TrimSpace(pluginName) != "" {
		title = fmt.Sprintf("%s components", pluginName)
	}
	if _, err := fmt.Fprintf(out, "%s\n\n", title); err != nil {
		return err
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tDEFAULT\tDISPOSITIONS\tSET FLAGS"); err != nil {
		return err
	}
	for _, def := range defs {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			def.Name,
			defaultDispositionLabel(def),
			dispositionListLabel(def),
			followUpFlagList(def),
		); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}

	for _, def := range defs {
		if err := writeComponentCatalogDetail(out, def); err != nil {
			return err
		}
	}
	return nil
}

func filterCatalogDefinitions(defs []Definition, componentName string) []Definition {
	componentName = strings.TrimSpace(componentName)
	if componentName == "" {
		return defs
	}
	out := make([]Definition, 0, 1)
	for _, def := range defs {
		if def.Name == componentName {
			out = append(out, def)
			break
		}
	}
	return out
}

func writeComponentCatalogDetail(out io.Writer, def Definition) error {
	if _, err := fmt.Fprintf(out, "\n%s\n", def.Name); err != nil {
		return err
	}
	writeCatalogLine := func(label, value string) error {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil
		}
		_, err := fmt.Fprintf(out, "  %s: %s\n", label, value)
		return err
	}

	if err := writeCatalogLine("default", defaultDispositionLabel(def)); err != nil {
		return err
	}
	if err := writeCatalogLine("dispositions", dispositionListLabel(def)); err != nil {
		return err
	}
	if err := writeCatalogLine("when enabled", strings.TrimSpace(RenderTransitionSummary(def.Behavior.Enable))); err != nil {
		return err
	}
	if err := writeCatalogLine("when disabled", strings.TrimSpace(RenderTransitionSummary(def.Behavior.Disable))); err != nil {
		return err
	}
	if err := writeCatalogGuidance(out, def); err != nil {
		return err
	}
	return writeCatalogFollowUps(out, def)
}

func writeCatalogGuidance(out io.Writer, def Definition) error {
	rows := []struct {
		label string
		value string
	}{
		{"enabled", def.Guidance.EnabledHelp},
		{"disabled", def.Guidance.DisabledHelp},
		{"superceded", def.Guidance.SupersededHelp},
		{"distributed", def.Guidance.DistributedHelp},
	}
	wrote := false
	for _, row := range rows {
		if strings.TrimSpace(row.value) == "" {
			continue
		}
		if !wrote {
			if _, err := fmt.Fprintln(out, "  guidance:"); err != nil {
				return err
			}
			wrote = true
		}
		if _, err := fmt.Fprintf(out, "    %s: %s\n", row.label, strings.TrimSpace(row.value)); err != nil {
			return err
		}
	}
	return nil
}

func writeCatalogFollowUps(out io.Writer, def Definition) error {
	if len(def.FollowUps) == 0 {
		return nil
	}
	wrote := false
	for _, followUp := range def.FollowUps {
		flagName := followUpFlagName(def.Name, followUp)
		if flagName == "" {
			continue
		}
		if !wrote {
			if _, err := fmt.Fprintln(out, "  set flags:"); err != nil {
				return err
			}
			wrote = true
		}
		usage := createFollowUpUsage(def.Name, followUp)
		condition := followUpCondition(followUp)
		if condition != "" {
			usage = fmt.Sprintf("%s (%s)", usage, condition)
		}
		if _, err := fmt.Fprintf(out, "    --%s: %s\n", flagName, usage); err != nil {
			return err
		}
		if choices := followUpChoicesLabel(followUp); choices != "" {
			if _, err := fmt.Fprintf(out, "      choices: %s\n", choices); err != nil {
				return err
			}
		}
		if strings.TrimSpace(followUp.DefaultValue) != "" {
			if _, err := fmt.Fprintf(out, "      default: %s\n", strings.TrimSpace(followUp.DefaultValue)); err != nil {
				return err
			}
		}
	}
	return nil
}

func defaultDispositionLabel(def Definition) string {
	if disposition := normalizeDisposition(def.DefaultDisposition); disposition != "" {
		return string(disposition)
	}
	if state := normalizeState(def.DefaultState); state != "" {
		return string(StateToDisposition(state))
	}
	return string(DispositionEnabled)
}

func dispositionListLabel(def Definition) string {
	if len(def.AllowedDispositions) == 0 {
		return "enabled, disabled"
	}
	values := make([]string, 0, len(def.AllowedDispositions))
	for _, disposition := range def.AllowedDispositions {
		if normalized := normalizeDisposition(disposition); normalized != "" {
			values = append(values, string(normalized))
		}
	}
	return strings.Join(values, ", ")
}

func followUpFlagList(def Definition) string {
	if len(def.FollowUps) == 0 {
		return "-"
	}
	values := make([]string, 0, len(def.FollowUps))
	seen := map[string]bool{}
	for _, followUp := range def.FollowUps {
		flagName := followUpFlagName(def.Name, followUp)
		if flagName == "" || seen[flagName] {
			continue
		}
		seen[flagName] = true
		values = append(values, "--"+flagName)
	}
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ", ")
}

func followUpCondition(followUp FollowUpSpec) string {
	switch {
	case normalizeDisposition(followUp.AppliesToDisposition) != "":
		return "when " + string(normalizeDisposition(followUp.AppliesToDisposition))
	case normalizeState(followUp.AppliesTo) != "":
		return "when " + string(normalizeState(followUp.AppliesTo))
	default:
		return ""
	}
}

func followUpChoicesLabel(followUp FollowUpSpec) string {
	if len(followUp.Choices) == 0 {
		return ""
	}
	values := make([]string, 0, len(followUp.Choices))
	for _, choice := range followUp.Choices {
		label := strings.TrimSpace(choice.Label)
		if label == "" {
			label = strings.TrimSpace(choice.Value)
		}
		if label == "__custom__" {
			label = "custom"
		}
		if label != "" {
			values = append(values, label)
		}
	}
	return strings.Join(values, ", ")
}
