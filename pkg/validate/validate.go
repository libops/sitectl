package validate

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/helpers"
	yaml "gopkg.in/yaml.v3"
)

const (
	StatusOK      = "ok"
	StatusFailed  = "failed"
	StatusWarning = "warning"
)

type Result struct {
	Name    string `json:"name" yaml:"name"`
	Status  string `json:"status" yaml:"status"`
	Detail  string `json:"detail,omitempty" yaml:"detail,omitempty"`
	FixHint string `json:"fix_hint,omitempty" yaml:"fix_hint,omitempty"`
}

type Report struct {
	Context     string   `json:"context" yaml:"context"`
	Site        string   `json:"site,omitempty" yaml:"site,omitempty"`
	Plugin      string   `json:"plugin,omitempty" yaml:"plugin,omitempty"`
	Environment string   `json:"environment,omitempty" yaml:"environment,omitempty"`
	Valid       bool     `json:"valid" yaml:"valid"`
	Results     []Result `json:"results" yaml:"results"`
}

type Validator func(*config.Context) ([]Result, error)

func Run(ctx *config.Context, validators ...Validator) ([]Result, error) {
	results := []Result{}
	for _, validator := range validators {
		if validator == nil {
			continue
		}
		out, err := validator(ctx)
		if err != nil {
			return nil, err
		}
		results = append(results, out...)
	}
	return results, nil
}

func NewReport(ctx *config.Context, results []Result) Report {
	report := Report{Results: results, Valid: true}
	if ctx != nil {
		report.Context = ctx.Name
		report.Site = ctx.Site
		report.Plugin = ctx.Plugin
		report.Environment = ctx.Environment
	}
	for _, result := range results {
		if result.Status == StatusFailed {
			report.Valid = false
			break
		}
	}
	return report
}

func WriteReports(out io.Writer, reports []Report, format string) error {
	switch normalizeFormat(format) {
	case corecomponent.ReportFormatSection:
		return writeSections(out, reports)
	case corecomponent.ReportFormatTable:
		return writeTable(out, reports)
	case corecomponent.ReportFormatJSON:
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(reports)
	case corecomponent.ReportFormatYAML:
		encoder := yaml.NewEncoder(out)
		defer encoder.Close()
		return encoder.Encode(reports)
	default:
		return fmt.Errorf("invalid report format %q: expected one of %s, %s, %s, %s",
			format,
			corecomponent.ReportFormatSection,
			corecomponent.ReportFormatTable,
			corecomponent.ReportFormatJSON,
			corecomponent.ReportFormatYAML,
		)
	}
}

func writeSections(out io.Writer, reports []Report) error {
	for i, report := range reports {
		title := helpers.FirstNonEmpty(report.Context, "validation")
		body := []string{
			fmt.Sprintf("Site: `%s`", helpers.FirstNonEmpty(report.Site, "-")),
			fmt.Sprintf("Plugin: `%s`", helpers.FirstNonEmpty(report.Plugin, "-")),
			fmt.Sprintf("Environment: `%s`", helpers.FirstNonEmpty(report.Environment, "-")),
			fmt.Sprintf("Valid: `%t`", report.Valid),
		}
		fmt.Fprintln(out, corecomponent.RenderSection(title, strings.Join(body, "\n")))
		for _, result := range report.Results {
			detail := strings.TrimSpace(result.Detail)
			if strings.TrimSpace(result.FixHint) != "" {
				if detail != "" {
					detail += ". "
				}
				detail += strings.TrimSpace(result.FixHint)
			}
			fmt.Fprintln(out, corecomponent.RenderChecklistItem(result.Name, result.Status, detail))
		}
		if i < len(reports)-1 {
			fmt.Fprintln(out)
		}
	}
	return nil
}

func writeTable(out io.Writer, reports []Report) error {
	writer := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(writer, "CONTEXT\tSITE\tPLUGIN\tENVIRONMENT\tCHECK\tSTATUS\tDETAIL")
	for _, report := range reports {
		for _, result := range report.Results {
			detail := strings.TrimSpace(result.Detail)
			if strings.TrimSpace(result.FixHint) != "" {
				if detail != "" {
					detail += " | "
				}
				detail += "fix: " + strings.TrimSpace(result.FixHint)
			}
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				helpers.FirstNonEmpty(report.Context, "-"),
				helpers.FirstNonEmpty(report.Site, "-"),
				helpers.FirstNonEmpty(report.Plugin, "-"),
				helpers.FirstNonEmpty(report.Environment, "-"),
				helpers.FirstNonEmpty(result.Name, "-"),
				helpers.FirstNonEmpty(result.Status, "-"),
				helpers.FirstNonEmpty(detail, "-"),
			)
		}
	}
	return writer.Flush()
}

func normalizeFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", corecomponent.ReportFormatSection:
		return corecomponent.ReportFormatSection
	case corecomponent.ReportFormatTable:
		return corecomponent.ReportFormatTable
	case corecomponent.ReportFormatJSON:
		return corecomponent.ReportFormatJSON
	case corecomponent.ReportFormatYAML:
		return corecomponent.ReportFormatYAML
	default:
		return strings.ToLower(strings.TrimSpace(format))
	}
}

func SortResults(results []Result) {
	sort.Slice(results, func(i, j int) bool {
		if results[i].Status != results[j].Status {
			return results[i].Status < results[j].Status
		}
		return results[i].Name < results[j].Name
	})
}
