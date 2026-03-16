package component

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRenderComponentStatusIncludesTransitionBehavior(t *testing.T) {
	view := ReviewView{
		Definition: Definition{
			Name: "blazegraph",
			Guidance: StateGuidance{
				OffHelp: "Remove triplestore indexing services.",
			},
			Behavior: Behavior{
				Enable: TransitionBehavior{
					Summary:       "Existing content may need a triplestore backfill after enabling.",
					DataMigration: DataMigrationBackfill,
				},
				Disable: TransitionBehavior{
					Summary:       "Disabling Blazegraph removes indexing integrations but does not require content migration.",
					DataMigration: DataMigrationNone,
				},
			},
		},
		Name:  "blazegraph",
		State: DetectedState(StateOff),
	}

	rendered := RenderComponentStatus(view)
	if !strings.Contains(rendered, "If enabled: Existing content may need a triplestore backfill after enabling.") {
		t.Fatalf("expected enable transition summary, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Impact: backfill likely required.") {
		t.Fatalf("expected migration impact, got:\n%s", rendered)
	}
}

func TestRunReviewCapturesExtraOptionsAndSummary(t *testing.T) {
	views := []ReviewView{
		{
			Definition: Definition{
				Name: "isle-tls",
				FollowUps: []FollowUpSpec{
					{
						Name:      "tls-mode",
						Label:     "TLS mode",
						AppliesTo: StateOn,
						Choices: []Choice{
							{Value: "mkcert", Label: "mkcert", Aliases: []string{"1"}},
						},
					},
				},
			},
			Name:           "isle-tls",
			State:          DetectedState(StateOff),
			FollowUpValues: map[string]string{"tls-mode": "mkcert"},
		},
	}

	var confirmedPrompt string
	decisions, err := RunReview(views, ReviewOptions{
		Input: func(question ...string) (string, error) { return "y", nil },
		PromptState: func(name string, guidance StateGuidance, input InputFunc) (State, error) {
			return StateOn, nil
		},
		PromptChoice: func(name string, choices []Choice, defaultValue string, input InputFunc, sections ...string) (string, error) {
			return "mkcert", nil
		},
		Confirm: func(prompt string) (bool, error) {
			confirmedPrompt = prompt
			return true, nil
		},
	})
	if err != nil {
		t.Fatalf("RunReview() error = %v", err)
	}

	if decisions["isle-tls"].State != StateOn {
		t.Fatalf("expected state on, got %q", decisions["isle-tls"].State)
	}
	if decisions["isle-tls"].Options["tls-mode"] != "mkcert" {
		t.Fatalf("expected tls-mode mkcert, got %q", decisions["isle-tls"].Options["tls-mode"])
	}
	if !strings.Contains(confirmedPrompt, "TLS mode: `mkcert`.") {
		t.Fatalf("expected summary prompt to include extra option, got:\n%s", confirmedPrompt)
	}
}

func TestAddReviewFlagsRegistersReportAndVerbose(t *testing.T) {
	var report bool
	var verbose bool
	var format string
	cmd := &cobra.Command{Use: "review"}

	AddReviewFlags(cmd, &report, &verbose, &format)

	if cmd.Flags().Lookup("report") == nil {
		t.Fatal("expected report flag to be registered")
	}
	if cmd.Flags().Lookup("verbose") == nil {
		t.Fatal("expected verbose flag to be registered")
	}
	if cmd.Flags().Lookup("format") == nil {
		t.Fatal("expected format flag to be registered")
	}
}

func TestWriteComponentStatusReportWithFormatTable(t *testing.T) {
	view := ReviewView{
		Definition: Definition{
			Name: "fcrepo",
			Guidance: StateGuidance{
				OnHelp: "Keep Fedora-backed storage.",
			},
			Behavior: Behavior{
				Enable:  TransitionBehavior{Summary: "Enable summary."},
				Disable: TransitionBehavior{Summary: "Disable summary."},
			},
		},
		Name:   "fcrepo",
		State:  DetectedState(StateOn),
		Detail: "mode=http",
	}

	var out strings.Builder
	if err := WriteComponentStatusReportWithFormat(&out, []ReviewView{view}, false, ReportFormatTable); err != nil {
		t.Fatalf("WriteComponentStatusReportWithFormat() error = %v", err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "COMPONENT") || !strings.Contains(rendered, "fcrepo") {
		t.Fatalf("expected table output, got:\n%s", rendered)
	}
}

func TestWriteComponentStatusReportWithFormatJSON(t *testing.T) {
	view := ReviewView{
		Definition: Definition{
			Name: "fcrepo",
			Behavior: Behavior{
				Enable:  TransitionBehavior{Summary: "Enable summary."},
				Disable: TransitionBehavior{Summary: "Disable summary."},
			},
		},
		Name:  "fcrepo",
		State: DetectedState(StateOff),
	}

	var out strings.Builder
	if err := WriteComponentStatusReportWithFormat(&out, []ReviewView{view}, false, ReportFormatJSON); err != nil {
		t.Fatalf("WriteComponentStatusReportWithFormat() error = %v", err)
	}
	if !strings.Contains(out.String(), "\"name\": \"fcrepo\"") {
		t.Fatalf("expected json output, got:\n%s", out.String())
	}
}

func TestBuildComponentStatusRowsIncludesFollowUps(t *testing.T) {
	view := ReviewView{
		Definition: Definition{
			Name: "fcrepo",
			FollowUps: []FollowUpSpec{
				{Name: "isle-file-system-uri", Label: "Drupal filesystem URI", AppliesTo: StateOff},
			},
		},
		Name:           "fcrepo",
		State:          DetectedState(StateOff),
		FollowUpValues: map[string]string{"isle-file-system-uri": "public"},
	}

	rows := BuildComponentStatusRows([]ReviewView{view}, false)
	if rows[0].FollowUps["isle-file-system-uri"] != "public" {
		t.Fatalf("expected follow-up in report rows, got %#v", rows[0].FollowUps)
	}
}

func TestWriteComponentStatusReportWithFormatRejectsInvalidFormat(t *testing.T) {
	view := ReviewView{
		Definition: Definition{Name: "fcrepo"},
		Name:       "fcrepo",
		State:      DetectedState(StateOn),
	}

	var out strings.Builder
	err := WriteComponentStatusReportWithFormat(&out, []ReviewView{view}, false, "csv")
	if err == nil {
		t.Fatal("expected invalid format error")
	}
	if !strings.Contains(err.Error(), "invalid report format") {
		t.Fatalf("expected invalid format error, got %v", err)
	}
}

func TestWriteComponentStatusReportWithFormatJSONIncludesVerboseDrift(t *testing.T) {
	view := ReviewView{
		Definition: Definition{
			Name:     "fcrepo",
			Behavior: Behavior{},
		},
		Name:        "fcrepo",
		State:       StateDrifted,
		DriftDetail: "uri mismatch",
		SDKStatus: &ComponentStatus{
			Name:  "fcrepo",
			State: StateDrifted,
			On: StateCheck{
				Results: []RuleCheckResult{
					{Domain: "compose", File: "docker-compose.yml", Detail: "expected value", Match: false},
				},
			},
		},
	}

	var out strings.Builder
	if err := WriteComponentStatusReportWithFormat(&out, []ReviewView{view}, true, ReportFormatJSON); err != nil {
		t.Fatalf("WriteComponentStatusReportWithFormat() error = %v", err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "\"drift_detail\": \"uri mismatch\"") {
		t.Fatalf("expected drift detail in json output, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "\"drift_checks\"") {
		t.Fatalf("expected drift checks in json output, got:\n%s", rendered)
	}
}
