package docs

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed index.md tour/*.md
var content embed.FS

type TourPane struct {
	Slug     string
	Title    string
	Markdown string
}

func LoadTour() ([]TourPane, error) {
	panes := make([]TourPane, 0, 4)

	indexData, err := content.ReadFile("index.md")
	if err != nil {
		return nil, fmt.Errorf("read embedded tour index: %w", err)
	}
	panes = append(panes, TourPane{
		Slug:     "index",
		Title:    firstHeading(string(indexData), "Tour"),
		Markdown: string(indexData),
	})

	entries, err := fs.ReadDir(content, "tour")
	if err != nil {
		return nil, fmt.Errorf("read embedded tour docs: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		path := filepath.Join("tour", name)
		data, err := content.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read embedded tour doc %q: %w", path, err)
		}
		markdown := string(data)
		panes = append(panes, TourPane{
			Slug:     strings.TrimSuffix(name, filepath.Ext(name)),
			Title:    firstHeading(markdown, strings.TrimSuffix(name, filepath.Ext(name))),
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
