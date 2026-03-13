package component

import (
	"fmt"
	"os"
	"strings"
)

type ComposeFile struct {
	path  string
	lines []string
}

func LoadComposeFile(path string) (*ComposeFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read compose file: %w", err)
	}
	return &ComposeFile{
		path:  path,
		lines: strings.Split(string(data), "\n"),
	}, nil
}

func (c *ComposeFile) Save() error {
	return os.WriteFile(c.path, []byte(strings.Join(c.lines, "\n")), 0o644)
}

func (c *ComposeFile) DeleteService(name string) error {
	return c.deleteSectionEntry("services", name)
}

func (c *ComposeFile) DeleteVolume(name string) error {
	return c.deleteSectionEntry("volumes", name)
}

func (c *ComposeFile) DeleteSectionEntry(section, key string) error {
	return c.deleteSectionEntry(section, key)
}

func (c *ComposeFile) DeleteServiceEnv(service, key string) error {
	serviceIdx, ok := c.findService(service)
	if !ok {
		return nil
	}
	envIdx, envStyle, ok := findEnvironmentBlock(c.lines, serviceIdx)
	if !ok || envStyle == envInlineEmpty {
		return nil
	}
	keyIdx, ok := findMapKey(c.lines, envIdx+1, key, 6)
	if !ok {
		return nil
	}
	end := findBlockEnd(c.lines, keyIdx, 6)
	c.lines = append(c.lines[:keyIdx], c.lines[end:]...)
	return nil
}

func (c *ComposeFile) SetServiceEnv(service, key, value string) error {
	serviceIdx, ok := c.findService(service)
	if !ok {
		return fmt.Errorf("service %q not found in compose file", service)
	}
	envIdx, envStyle, ok := findEnvironmentBlock(c.lines, serviceIdx)
	if !ok {
		insertAt := findBlockEnd(c.lines, serviceIdx, 2)
		block := []string{
			"    environment:",
			fmt.Sprintf("      %s: %q", key, value),
		}
		c.lines = insertLines(c.lines, insertAt, block)
		return nil
	}

	switch envStyle {
	case envInlineEmpty:
		c.lines[envIdx] = "    environment:"
		c.lines = insertLines(c.lines, envIdx+1, []string{fmt.Sprintf("      %s: %q", key, value)})
		return nil
	case envBlock:
		if keyIdx, ok := findMapKey(c.lines, envIdx+1, key, 6); ok {
			c.lines[keyIdx] = fmt.Sprintf("      %s: %q", key, value)
			return nil
		}
		insertAt := findBlockEnd(c.lines, envIdx, 4)
		c.lines = insertLines(c.lines, insertAt, []string{fmt.Sprintf("      %s: %q", key, value)})
		return nil
	default:
		return fmt.Errorf("unsupported environment style for service %q", service)
	}
}

func (c *ComposeFile) deleteSectionEntry(section, key string) error {
	sectionIdx, ok := findMapKey(c.lines, 0, section, 0)
	if !ok {
		return nil
	}
	entryIdx, ok := findMapKey(c.lines, sectionIdx+1, key, 2)
	if !ok {
		return nil
	}
	end := findBlockEnd(c.lines, entryIdx, 2)
	c.lines = append(c.lines[:entryIdx], c.lines[end:]...)
	return nil
}

func (c *ComposeFile) findService(service string) (int, bool) {
	servicesIdx, ok := findMapKey(c.lines, 0, "services", 0)
	if !ok {
		return 0, false
	}
	return findMapKey(c.lines, servicesIdx+1, service, 2)
}

type envBlockStyle int

const (
	envBlock envBlockStyle = iota
	envInlineEmpty
)

func findEnvironmentBlock(lines []string, serviceIdx int) (int, envBlockStyle, bool) {
	serviceEnd := findBlockEnd(lines, serviceIdx, 2)
	for i := serviceIdx + 1; i < serviceEnd; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "environment:" {
			return i, envBlock, true
		}
		if trimmed == "environment: {}" {
			return i, envInlineEmpty, true
		}
	}
	return 0, envBlock, false
}

func findMapKey(lines []string, start int, key string, indent int) (int, bool) {
	prefix := strings.Repeat(" ", indent) + key + ":"
	for i := start; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		currentIndent := leadingSpaces(line)
		if currentIndent < indent {
			break
		}
		if currentIndent == indent && strings.HasPrefix(line, prefix) {
			return i, true
		}
	}
	return 0, false
}

func findBlockEnd(lines []string, start int, indent int) int {
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if leadingSpaces(line) <= indent {
			return i
		}
	}
	return len(lines)
}

func insertLines(lines []string, index int, inserted []string) []string {
	result := make([]string, 0, len(lines)+len(inserted))
	result = append(result, lines[:index]...)
	result = append(result, inserted...)
	result = append(result, lines[index:]...)
	return result
}

func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}
