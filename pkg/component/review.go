package component

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type ReviewView struct {
	Definition  Definition
	Name        string
	State       DetectedState
	Detail      string
	DriftDetail string
	SDKStatus   *ComponentStatus
	Extra       any
}

type ReviewDecision struct {
	State   State
	Options map[string]string
}

type PromptStateFunc func(name string, guidance StateGuidance, input InputFunc) (State, error)
type SummaryLineFunc func(view ReviewView, decision ReviewDecision) (string, error)
type ReviewPromptExtraFunc func(view ReviewView, decision *ReviewDecision) error
type ReviewConfirmFunc func(prompt string) (bool, error)

type ReviewOptions struct {
	Input       InputFunc
	PromptState PromptStateFunc
	PromptExtra ReviewPromptExtraFunc
	SummaryLine SummaryLineFunc
	Confirm     ReviewConfirmFunc
}

type ReviewMode struct {
	Report  bool
	Verbose bool
}

const (
	ReportFormatSection = "section"
	ReportFormatTable   = "table"
	ReportFormatJSON    = "json"
	ReportFormatYAML    = "yaml"
)

type ReportRow struct {
	Name            string   `json:"name" yaml:"name"`
	State           string   `json:"state" yaml:"state"`
	DetectedMode    string   `json:"detected_mode,omitempty" yaml:"detected_mode,omitempty"`
	CurrentGuidance string   `json:"current_guidance,omitempty" yaml:"current_guidance,omitempty"`
	IfEnabled       string   `json:"if_enabled" yaml:"if_enabled"`
	IfDisabled      string   `json:"if_disabled" yaml:"if_disabled"`
	DriftDetail     string   `json:"drift_detail,omitempty" yaml:"drift_detail,omitempty"`
	DriftChecks     []string `json:"drift_checks,omitempty" yaml:"drift_checks,omitempty"`
}

func AddReviewFlags(cmd *cobra.Command, reportTarget, verboseTarget *bool, formatTarget *string) {
	if reportTarget != nil {
		cmd.Flags().BoolVar(reportTarget, "report", false, "Print the current component report without prompting or applying changes")
	}
	AddReportFlags(cmd, verboseTarget, formatTarget)
}

func AddReportFlags(cmd *cobra.Command, verboseTarget *bool, formatTarget *string) {
	if verboseTarget != nil {
		cmd.Flags().BoolVar(verboseTarget, "verbose", false, "Show drift details in report output")
	}
	if formatTarget != nil {
		cmd.Flags().StringVar(formatTarget, "format", ReportFormatSection, "Report output format: section, table, json, or yaml")
	}
}

func RenderComponentStatus(view ReviewView) string {
	lines := []string{
		fmt.Sprintf("Current state: `%s`", view.State),
	}
	if strings.TrimSpace(view.Detail) != "" {
		lines = append(lines, fmt.Sprintf("Detected mode: %s", view.Detail))
	}
	if guidance := RenderCurrentGuidance(view); guidance != "" {
		lines = append(lines, "", guidance)
	}
	lines = append(lines,
		"",
		fmt.Sprintf("If enabled: %s", RenderTransitionSummary(view.Definition.Behavior.Enable)),
		fmt.Sprintf("If disabled: %s", RenderTransitionSummary(view.Definition.Behavior.Disable)),
	)
	return RenderSection(view.Name, strings.Join(lines, "\n"))
}

func BuildReviewQuestion(view ReviewView) string {
	lines := []string{
		fmt.Sprintf("Current state: `%s`", view.State),
	}
	if strings.TrimSpace(view.Detail) != "" {
		lines = append(lines, fmt.Sprintf("Detected mode: %s", view.Detail))
	}
	if guidance := RenderCurrentGuidance(view); guidance != "" {
		lines = append(lines, "", guidance)
	}
	if view.State == StateDrifted && strings.TrimSpace(view.DriftDetail) != "" {
		lines = append(lines, "", "Current config is drifted and does not match a clean on/off state.")
	}
	if strings.TrimSpace(view.Definition.Guidance.Question) != "" {
		lines = append(lines, "", strings.TrimSpace(view.Definition.Guidance.Question))
	}
	lines = append(lines,
		"",
		fmt.Sprintf("If enabled: %s", RenderTransitionSummary(view.Definition.Behavior.Enable)),
		fmt.Sprintf("If disabled: %s", RenderTransitionSummary(view.Definition.Behavior.Disable)),
	)
	return strings.Join(lines, "\n")
}

func ReviewDefaultState(view ReviewView) State {
	switch view.State {
	case DetectedState(StateOn):
		return StateOn
	case DetectedState(StateOff):
		return StateOff
	default:
		if view.Definition.DefaultState != "" {
			return view.Definition.DefaultState
		}
		return StateOff
	}
}

func RenderCurrentGuidance(view ReviewView) string {
	switch view.State {
	case DetectedState(StateOn):
		return strings.TrimSpace(view.Definition.Guidance.OnHelp)
	case DetectedState(StateOff):
		return strings.TrimSpace(view.Definition.Guidance.OffHelp)
	default:
		if strings.TrimSpace(view.Definition.Guidance.Question) != "" {
			return strings.TrimSpace(view.Definition.Guidance.Question)
		}
		return "This component does not match a clean on/off state right now."
	}
}

func RenderTransitionSummary(behavior TransitionBehavior) string {
	summary := strings.TrimSpace(behavior.Summary)
	impact := RenderMigrationImpact(behavior.DataMigration)
	switch {
	case summary == "" && impact == "":
		return "No additional behavior recorded."
	case summary == "":
		return impact + "."
	case impact == "":
		return summary
	default:
		return fmt.Sprintf("%s Impact: %s.", summary, impact)
	}
}

func RenderMigrationImpact(migration DataMigrationRequirement) string {
	switch migration {
	case "", DataMigrationNone:
		return "low consequence"
	case DataMigrationBackfill:
		return "backfill likely required"
	case DataMigrationHard:
		return "high consequence, plan a data migration first"
	default:
		return string(migration)
	}
}

func RunReview(views []ReviewView, opts ReviewOptions) (map[string]ReviewDecision, error) {
	input := opts.Input
	if input == nil {
		input = config.GetInput
	}
	promptState := opts.PromptState
	if promptState == nil {
		promptState = PromptState
	}

	decisions := make(map[string]ReviewDecision, len(views))
	for _, view := range views {
		guidance := view.Definition.Guidance
		guidance.DefaultState = ReviewDefaultState(view)
		guidance.Question = BuildReviewQuestion(view)

		state, err := promptState(view.Name, guidance, input)
		if err != nil {
			return nil, err
		}

		decision := ReviewDecision{State: state, Options: map[string]string{}}
		if opts.PromptExtra != nil {
			if err := opts.PromptExtra(view, &decision); err != nil {
				return nil, err
			}
		}
		decisions[view.Name] = decision
	}

	summary, err := RenderReviewSummary(views, decisions, opts.SummaryLine)
	if err != nil {
		return nil, err
	}

	confirmed := false
	if opts.Confirm != nil {
		confirmed, err = opts.Confirm(summary)
	} else {
		confirmed, err = defaultConfirmReview(summary, input)
	}
	if err != nil {
		return nil, err
	}
	if !confirmed {
		return nil, fmt.Errorf("component review cancelled")
	}

	return decisions, nil
}

func RenderReviewSummary(views []ReviewView, decisions map[string]ReviewDecision, summaryLine SummaryLineFunc) (string, error) {
	lines := []string{}
	for _, view := range views {
		decision, ok := decisions[view.Name]
		if !ok {
			return "", fmt.Errorf("missing review decision for %q", view.Name)
		}
		line := fmt.Sprintf("Set `%s` to `%s`.", view.Name, decision.State)
		var err error
		if summaryLine != nil {
			line, err = summaryLine(view, decision)
			if err != nil {
				return "", err
			}
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", "This updates docker compose and Drupal config.")
	section := RenderSection("Confirm component review", strings.Join(lines, "\n"))
	return section + "\n\n" + RenderPromptLine("Apply review? [y/N]: "), nil
}

func defaultConfirmReview(prompt string, input InputFunc) (bool, error) {
	response, err := input(prompt)
	if err != nil {
		return false, err
	}
	value := strings.TrimSpace(strings.ToLower(response))
	return value == "y" || value == "yes", nil
}

func WriteDriftDetails(out io.Writer, status ComponentStatus) {
	printedHeader := false
	printFailures := func(label string, check StateCheck) {
		for _, result := range check.Results {
			if result.Match {
				continue
			}
			if !printedHeader {
				fmt.Fprintln(out, "  drift:")
				printedHeader = true
			}
			fmt.Fprintf(out, "    %s %s %s %s\n", label, result.Domain, result.File, strings.TrimSpace(result.Detail))
		}
	}
	printFailures("expected on:", status.On)
	printFailures("expected off:", status.Off)
}

func WriteComponentStatusReport(out io.Writer, views []ReviewView, verbose bool) error {
	return WriteComponentStatusReportWithFormat(out, views, verbose, ReportFormatSection)
}

func WriteComponentStatusReportWithFormat(out io.Writer, views []ReviewView, verbose bool, format string) error {
	rows := BuildComponentStatusRows(views, verbose)
	switch normalizeReportFormat(format) {
	case ReportFormatSection:
		return writeComponentStatusSections(out, views, verbose)
	case ReportFormatTable:
		return writeComponentStatusTable(out, rows)
	case ReportFormatJSON:
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(rows)
	case ReportFormatYAML:
		encoder := yaml.NewEncoder(out)
		defer encoder.Close()
		return encoder.Encode(rows)
	default:
		return fmt.Errorf("invalid report format %q: expected one of %s, %s, %s, %s", format, ReportFormatSection, ReportFormatTable, ReportFormatJSON, ReportFormatYAML)
	}
}

func BuildComponentStatusRows(views []ReviewView, verbose bool) []ReportRow {
	rows := make([]ReportRow, 0, len(views))
	for _, view := range views {
		row := ReportRow{
			Name:            view.Name,
			State:           string(view.State),
			DetectedMode:    strings.TrimSpace(view.Detail),
			CurrentGuidance: RenderCurrentGuidance(view),
			IfEnabled:       RenderTransitionSummary(view.Definition.Behavior.Enable),
			IfDisabled:      RenderTransitionSummary(view.Definition.Behavior.Disable),
		}
		if verbose && view.State == StateDrifted {
			row.DriftDetail = strings.TrimSpace(view.DriftDetail)
			row.DriftChecks = DriftCheckLines(view)
		}
		rows = append(rows, row)
	}
	return rows
}

func writeComponentStatusSections(out io.Writer, views []ReviewView, verbose bool) error {
	for i, view := range views {
		fmt.Fprintln(out, RenderComponentStatus(view))
		if verbose && view.State == StateDrifted {
			if view.SDKStatus != nil {
				WriteDriftDetails(out, *view.SDKStatus)
			} else if strings.TrimSpace(view.DriftDetail) != "" {
				fmt.Fprintln(out, "  drift:")
				fmt.Fprintf(out, "    %s\n", strings.TrimSpace(view.DriftDetail))
			}
		}
		if i < len(views)-1 {
			fmt.Fprintln(out)
		}
	}
	return nil
}

func writeComponentStatusTable(out io.Writer, rows []ReportRow) error {
	writer := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(writer, "COMPONENT\tSTATE\tMODE\tCURRENT\tIF ENABLED\tIF DISABLED")
	for _, row := range rows {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
			row.Name,
			row.State,
			fallbackReportValue(row.DetectedMode),
			fallbackReportValue(row.CurrentGuidance),
			fallbackReportValue(row.IfEnabled),
			fallbackReportValue(row.IfDisabled),
		)
		if len(row.DriftChecks) > 0 {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
				"",
				"",
				"",
				"drift: "+strings.Join(row.DriftChecks, " | "),
				"",
				"",
			)
		} else if strings.TrimSpace(row.DriftDetail) != "" {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
				"",
				"",
				"",
				"drift: "+row.DriftDetail,
				"",
				"",
			)
		}
	}
	return writer.Flush()
}

func DriftCheckLines(view ReviewView) []string {
	if view.SDKStatus == nil {
		return nil
	}
	lines := []string{}
	appendFailures := func(label string, check StateCheck) {
		for _, result := range check.Results {
			if result.Match {
				continue
			}
			lines = append(lines, fmt.Sprintf("%s %s %s %s", label, result.Domain, result.File, strings.TrimSpace(result.Detail)))
		}
	}
	appendFailures("expected on:", view.SDKStatus.On)
	appendFailures("expected off:", view.SDKStatus.Off)
	return lines
}

func normalizeReportFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", ReportFormatSection:
		return ReportFormatSection
	case ReportFormatTable:
		return ReportFormatTable
	case ReportFormatJSON:
		return ReportFormatJSON
	case ReportFormatYAML:
		return ReportFormatYAML
	default:
		return strings.ToLower(strings.TrimSpace(format))
	}
}

func fallbackReportValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return strings.TrimSpace(value)
}
