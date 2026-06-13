package format

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"text/template"
)

// OutputFormat represents the output format type.
type OutputFormat struct {
	Type     string // "table", "json", or "template"
	Template string // template string for custom formats
}

// ParseFormat parses the --format flag value.
// Examples:
//   - "" or "table" -> table format with default template
//   - "table TEMPLATE" -> table format with custom Go template
//   - "json" -> JSON format
//   - "TEMPLATE" -> custom Go template
func ParseFormat(formatStr string) (*OutputFormat, error) {
	if formatStr == "" || formatStr == "table" {
		return &OutputFormat{Type: "table"}, nil
	}

	if formatStr == "json" {
		return &OutputFormat{Type: "json"}, nil
	}

	if strings.HasPrefix(formatStr, "table ") {
		tmpl := strings.TrimPrefix(formatStr, "table ")
		if tmpl == "" {
			return nil, fmt.Errorf("table format requires a template")
		}
		return &OutputFormat{Type: "table", Template: tmpl}, nil
	}

	return &OutputFormat{Type: "template", Template: formatStr}, nil
}

// Formatter handles formatting and outputting data.
type Formatter struct {
	format *OutputFormat
	writer io.Writer
}

// NewFormatter creates a new formatter.
func NewFormatter(formatStr string) (*Formatter, error) {
	format, err := ParseFormat(formatStr)
	if err != nil {
		return nil, err
	}

	return &Formatter{
		format: format,
		writer: os.Stdout,
	}, nil
}

// Print formats and prints the data according to the format specification.
// For table format, headers and rows should be provided.
// For JSON and template formats, data should be the object to format.
func (f *Formatter) Print(data interface{}, headers []string, rows [][]string) error {
	switch f.format.Type {
	case "table":
		return f.printTable(data, headers, rows)
	case "json":
		return f.printJSON(data)
	case "template":
		return f.printTemplate(data)
	default:
		return fmt.Errorf("unknown format type: %s", f.format.Type)
	}
}

func (f *Formatter) printTable(data interface{}, headers []string, rows [][]string) error {
	if f.format.Template != "" {
		return f.printTableWithTemplate(data)
	}

	w := tabwriter.NewWriter(f.writer, 0, 0, 3, ' ', 0)
	defer w.Flush()

	if len(headers) > 0 {
		fmt.Fprintln(w, strings.Join(headers, "\t"))
		separators := make([]string, len(headers))
		for i, h := range headers {
			separators[i] = strings.Repeat("-", len(h))
		}
		fmt.Fprintln(w, strings.Join(separators, "\t"))
	}

	for _, row := range rows {
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}

	return nil
}

func (f *Formatter) printTableWithTemplate(data interface{}) error {
	tmpl, err := template.New("table").Parse(f.format.Template)
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	switch v := data.(type) {
	case []interface{}:
		for _, item := range v {
			if err := tmpl.Execute(f.writer, item); err != nil {
				return fmt.Errorf("failed to execute template: %w", err)
			}
			fmt.Fprintln(f.writer)
		}
	default:
		if err := tmpl.Execute(f.writer, data); err != nil {
			return fmt.Errorf("failed to execute template: %w", err)
		}
		fmt.Fprintln(f.writer)
	}

	return nil
}

func (f *Formatter) printJSON(data interface{}) error {
	encoder := json.NewEncoder(f.writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(data)
}

func (f *Formatter) printTemplate(data interface{}) error {
	tmpl, err := template.New("custom").Parse(f.format.Template)
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	if err := tmpl.Execute(f.writer, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	fmt.Fprintln(f.writer)
	return nil
}
