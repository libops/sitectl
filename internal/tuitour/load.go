package tuitour

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed content/*.md
var content embed.FS

type Pane struct {
	Slug     string
	Title    string
	Markdown string
}

func Load() ([]Pane, error) {
	paths := []string{
		"content/index.md",
		"content/tui.md",
		"content/context.md",
		"content/plugins.md",
		"content/components.md",
	}

	panes := make([]Pane, 0, len(paths))
	for _, path := range paths {
		data, err := content.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read embedded tour doc %q: %w", path, err)
		}

		markdown := string(data)
		slug := strings.TrimSuffix(strings.TrimPrefix(path, "content/"), ".md")
		panes = append(panes, Pane{
			Slug:     slug,
			Title:    firstHeading(markdown, slug),
			Markdown: markdown,
		})
	}

	return panes, nil
}

func firstHeading(markdown, fallback string) string {
	for line := range strings.SplitSeq(markdown, "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "# "); ok {
			return strings.TrimSpace(after)
		}
	}
	return strings.TrimSpace(fallback)
}
