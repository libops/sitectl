package component

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/libops/sitectl/pkg/config"
	yaml "gopkg.in/yaml.v3"
)

// ChangeRisk describes the operational seriousness of a planned change.
type ChangeRisk string

const (
	RiskLow         ChangeRisk = "low"
	RiskRestart     ChangeRisk = "restart"
	RiskDestructive ChangeRisk = "destructive"
	RiskUnknown     ChangeRisk = "unknown"
)

// PlannedChange is one component-owned difference from durable desired state.
type PlannedChange struct {
	Component string     `json:"component" yaml:"component"`
	Domain    string     `json:"domain" yaml:"domain"`
	File      string     `json:"file,omitempty" yaml:"file,omitempty"`
	Operation RuleOp     `json:"operation" yaml:"operation"`
	Path      string     `json:"path,omitempty" yaml:"path,omitempty"`
	Detail    string     `json:"detail,omitempty" yaml:"detail,omitempty"`
	Risk      ChangeRisk `json:"risk" yaml:"risk"`
}

// ComponentPlan reports one component's desired and observed disposition.
type ComponentPlan struct {
	Name      string             `json:"name" yaml:"name"`
	Desired   Disposition        `json:"desired" yaml:"desired"`
	Observed  DetectedState      `json:"observed" yaml:"observed"`
	InSync    bool               `json:"in_sync" yaml:"in_sync"`
	Changes   []PlannedChange    `json:"changes,omitempty" yaml:"changes,omitempty"`
	Unknowns  []string           `json:"unknowns,omitempty" yaml:"unknowns,omitempty"`
	Selection ComponentSelection `json:"selection" yaml:"selection"`
}

// ReconciliationPlan is the single report consumed by planning, validation, and convergence.
type ReconciliationPlan struct {
	Context    string          `json:"context" yaml:"context"`
	Site       string          `json:"site,omitempty" yaml:"site,omitempty"`
	Plugin     string          `json:"plugin" yaml:"plugin"`
	Safe       bool            `json:"safe" yaml:"safe"`
	InSync     bool            `json:"in_sync" yaml:"in_sync"`
	Components []ComponentPlan `json:"components" yaml:"components"`
	Unknowns   []string        `json:"unknowns,omitempty" yaml:"unknowns,omitempty"`
}

// BuildReconciliationPlan compares registered component contracts with durable intent.
func BuildReconciliationPlan(ctx *config.Context, projectRoot string, desired DesiredState, opts DetectOptions, defs ...Definition) (ReconciliationPlan, error) {
	if err := desired.Validate(ctx.Plugin); err != nil {
		return ReconciliationPlan{}, err
	}
	plan := ReconciliationPlan{Context: ctx.Name, Site: ctx.Site, Plugin: ctx.Plugin, Safe: true, InSync: true}
	defByName := make(map[string]Definition, len(defs))
	for _, def := range defs {
		defByName[def.Name] = def
	}
	for name := range desired.Spec.Components {
		if _, ok := defByName[name]; !ok {
			plan.Safe = false
			plan.InSync = false
			plan.Unknowns = append(plan.Unknowns, fmt.Sprintf("desired component %q is not registered by plugin %q", name, ctx.Plugin))
		}
	}
	for _, def := range defs {
		selection, ok := desired.Spec.Components[def.Name]
		componentPlan := ComponentPlan{Name: def.Name, Selection: selection}
		if !ok {
			componentPlan.Unknowns = append(componentPlan.Unknowns, "component has no persisted desired disposition")
			plan.Safe = false
			plan.InSync = false
			plan.Components = append(plan.Components, componentPlan)
			continue
		}
		disposition, err := ResolveAllowedDisposition(def.AllowedDispositions, selection.Disposition)
		if err != nil {
			componentPlan.Unknowns = append(componentPlan.Unknowns, err.Error())
			plan.Safe = false
			plan.InSync = false
			plan.Components = append(plan.Components, componentPlan)
			continue
		}
		componentPlan.Desired = disposition
		status, err := DetectComponentStatus(ctx, projectRoot, def, opts)
		if err != nil {
			return ReconciliationPlan{}, fmt.Errorf("inspect component %q: %w", def.Name, err)
		}
		componentPlan.Observed = status.State
		check := status.On
		if DispositionToState(disposition) == StateOff {
			check = status.Off
		}
		for _, result := range check.Results {
			if result.Match {
				continue
			}
			componentPlan.Changes = append(componentPlan.Changes, PlannedChange{
				Component: def.Name,
				Domain:    result.Domain,
				File:      result.File,
				Operation: result.Op,
				Path:      result.Path,
				Detail:    redactPlanDetail(result.Detail),
				Risk:      riskForChange(result),
			})
		}
		componentPlan.InSync = len(componentPlan.Changes) == 0
		if !componentPlan.InSync {
			plan.InSync = false
		}
		plan.Components = append(plan.Components, componentPlan)
	}
	sort.Slice(plan.Components, func(i, j int) bool { return plan.Components[i].Name < plan.Components[j].Name })
	sort.Strings(plan.Unknowns)
	return plan, nil
}

// FilterReconciliationPlan returns a component-scoped view without weakening global safety findings.
func FilterReconciliationPlan(plan ReconciliationPlan, name string) (ReconciliationPlan, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return plan, nil
	}
	for _, component := range plan.Components {
		if component.Name == name {
			plan.Components = []ComponentPlan{component}
			plan.InSync = component.InSync && len(plan.Unknowns) == 0
			plan.Safe = plan.Safe && len(component.Unknowns) == 0
			return plan, nil
		}
	}
	return ReconciliationPlan{}, fmt.Errorf("unknown component %q", name)
}

func riskForChange(result RuleCheckResult) ChangeRisk {
	switch result.Op {
	case OpDelete:
		return RiskDestructive
	case OpSet, OpRestore, OpReplace:
		if result.Domain == "compose" {
			return RiskRestart
		}
		return RiskLow
	case OpContains, OpNotContains:
		return RiskLow
	default:
		return RiskUnknown
	}
}

func redactPlanDetail(detail string) string {
	lower := strings.ToLower(detail)
	for _, sensitive := range []string{"password", "passwd", "secret", "token", "private_key", "api_key"} {
		if strings.Contains(lower, sensitive) {
			return "value differs (sensitive detail hidden)"
		}
	}
	return detail
}

// WriteReconciliationPlan renders a plan for people or automation.
func WriteReconciliationPlan(out io.Writer, plan ReconciliationPlan, format string) error {
	outputPlan := redactReconciliationPlan(plan)
	switch normalizePlanFormat(format) {
	case ReportFormatJSON:
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(outputPlan)
	case ReportFormatYAML:
		return yaml.NewEncoder(out).Encode(outputPlan)
	case ReportFormatTable:
		writer := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		fmt.Fprintln(writer, "COMPONENT\tDESIRED\tOBSERVED\tSTATUS\tRISK\tCHANGE")
		for _, component := range plan.Components {
			if len(component.Unknowns) > 0 {
				fmt.Fprintf(writer, "%s\t%s\t%s\tUNKNOWN\t%s\t%s\n", component.Name, component.Desired, component.Observed, RiskUnknown, strings.Join(component.Unknowns, "; "))
			}
			if len(component.Changes) == 0 && len(component.Unknowns) == 0 {
				fmt.Fprintf(writer, "%s\t%s\t%s\tIN SYNC\t-\t-\n", component.Name, component.Desired, component.Observed)
			}
			for _, change := range component.Changes {
				target := strings.Trim(strings.Join([]string{change.Domain, change.File, change.Path}, "/"), "/")
				fmt.Fprintf(writer, "%s\t%s\t%s\tCHANGE\t%s\t%s %s\n", component.Name, component.Desired, component.Observed, change.Risk, change.Operation, target)
			}
		}
		return writer.Flush()
	default:
		fmt.Fprintln(out, RenderSection("Reconciliation plan", fmt.Sprintf("Context: `%s`\nDesired state is reproducible: `%t`\nComponents are in sync: `%t`", plan.Context, plan.Safe, plan.InSync)))
		for _, component := range plan.Components {
			status := "ok"
			if !component.InSync {
				status = "warning"
			}
			fmt.Fprintln(out, RenderChecklistItem(component.Name, status, fmt.Sprintf("desired %s; observed %s", component.Desired, component.Observed)))
			for _, unknown := range component.Unknowns {
				fmt.Fprintln(out, "  "+RenderChecklistItem("unknown", "failed", unknown))
			}
			for _, change := range component.Changes {
				target := strings.Trim(strings.Join([]string{change.Domain, change.File, change.Path}, "/"), "/")
				fmt.Fprintf(out, "  %s %s [%s]\n", strings.ToUpper(string(change.Operation)), target, change.Risk)
				if strings.TrimSpace(change.Detail) != "" {
					fmt.Fprintf(out, "    %s\n", change.Detail)
				}
			}
		}
		for _, unknown := range plan.Unknowns {
			fmt.Fprintln(out, RenderChecklistItem("unknown", "failed", unknown))
		}
		return nil
	}
}

func redactReconciliationPlan(plan ReconciliationPlan) ReconciliationPlan {
	for i := range plan.Components {
		settings := plan.Components[i].Selection.Settings
		if len(settings) == 0 {
			continue
		}
		redacted := make(map[string]string, len(settings))
		for key, value := range settings {
			if sensitivePlanKey(key) {
				redacted[key] = "<redacted>"
			} else {
				redacted[key] = value
			}
		}
		plan.Components[i].Selection.Settings = redacted
	}
	return plan
}

func sensitivePlanKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	for _, sensitive := range []string{"password", "passwd", "secret", "token", "private_key", "api_key"} {
		if strings.Contains(key, sensitive) {
			return true
		}
	}
	return false
}

func normalizePlanFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", ReportFormatSection:
		return ReportFormatSection
	case ReportFormatTable, ReportFormatJSON, ReportFormatYAML:
		return strings.ToLower(strings.TrimSpace(format))
	default:
		return strings.ToLower(strings.TrimSpace(format))
	}
}
