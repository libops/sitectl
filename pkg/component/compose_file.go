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

func LoadComposeFileOptional(path string) (*ComposeFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ComposeFile{
				path:  path,
				lines: nil,
			}, nil
		}
		return nil, fmt.Errorf("read compose file: %w", err)
	}
	return &ComposeFile{
		path:  path,
		lines: strings.Split(string(data), "\n"),
	}, nil
}

func (c *ComposeFile) Save() error {
	if len(composeContentLines(c.lines)) == 0 {
		if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(c.path, []byte(strings.Join(c.lines, "\n")), 0o644)
}

func (c *ComposeFile) DeleteService(name string) error {
	return c.deleteSectionEntry("services", name)
}

func (c *ComposeFile) DeleteVolume(name string) error {
	return c.deleteSectionEntry("volumes", name)
}

func (c *ComposeFile) HasService(name string) bool {
	_, ok := c.findService(name)
	return ok
}

func (c *ComposeFile) HasVolume(name string) bool {
	_, ok := c.findSectionEntry("volumes", name)
	return ok
}

func (c *ComposeFile) ServiceBlock(name string) (string, bool) {
	return c.sectionEntryBlock("services", name)
}

func (c *ComposeFile) VolumeBlock(name string) (string, bool) {
	return c.sectionEntryBlock("volumes", name)
}

func (c *ComposeFile) AddServiceBlock(name, block string) error {
	return c.addSectionEntryBlock("services", name, block)
}

func (c *ComposeFile) AddVolumeBlock(name, block string) error {
	return c.addSectionEntryBlock("volumes", name, block)
}

func (c *ComposeFile) addSectionEntryBlock(section, name, block string) error {
	if strings.TrimSpace(block) == "" {
		return fmt.Errorf("%s block for %q is empty", section, name)
	}
	if _, ok := c.findSectionEntry(section, name); ok {
		return nil
	}

	sectionIdx, ok := findMapKey(c.lines, 0, section, 0)
	if !ok {
		c.lines = append(c.lines, section+":")
		sectionIdx = len(c.lines) - 1
	}
	insertAt := findBlockEnd(c.lines, sectionIdx, 0)
	c.lines = insertLines(c.lines, insertAt, strings.Split(strings.TrimRight(block, "\n"), "\n"))
	return nil
}

func (c *ComposeFile) SetServiceStringList(service, key string, values []string) error {
	serviceIdx, ok := c.findService(service)
	if !ok {
		return fmt.Errorf("service %q not found in compose file", service)
	}
	keyIdx, ok := findMapKey(c.lines, serviceIdx+1, key, 4)
	listBlock := []string{"    " + key + ":"}
	for _, value := range values {
		listBlock = append(listBlock, fmt.Sprintf("      - %s", value))
	}
	if ok {
		end := findBlockEnd(c.lines, keyIdx, 4)
		c.lines = append(c.lines[:keyIdx], append(listBlock, c.lines[end:]...)...)
		return nil
	}
	insertAt := findBlockEnd(c.lines, serviceIdx, 2)
	c.lines = insertLines(c.lines, insertAt, listBlock)
	return nil
}

func (c *ComposeFile) DeleteServiceKey(service, key string) error {
	serviceIdx, ok := c.findService(service)
	if !ok {
		return nil
	}
	keyIdx, ok := findMapKey(c.lines, serviceIdx+1, key, 4)
	if !ok {
		return nil
	}
	end := findBlockEnd(c.lines, keyIdx, 4)
	c.lines = append(c.lines[:keyIdx], c.lines[end:]...)
	return nil
}

func (c *ComposeFile) DeleteSectionEntry(section, key string) error {
	return c.deleteSectionEntry(section, key)
}

func (c *ComposeFile) SectionEntryBlock(section, key string) (string, bool) {
	sectionIdx, ok := findMapKey(c.lines, 0, section, 0)
	if !ok {
		return "", false
	}
	entryIdx, ok := findMapKey(c.lines, sectionIdx+1, key, 2)
	if !ok {
		return "", false
	}
	end := findBlockEnd(c.lines, entryIdx, 2)
	return strings.Join(c.lines[entryIdx:end], "\n"), true
}

func (c *ComposeFile) AddSectionEntryBlock(section, key, block string) error {
	if strings.TrimSpace(block) == "" {
		return fmt.Errorf("section block for %s.%s is empty", section, key)
	}
	if _, ok := c.SectionEntryBlock(section, key); ok {
		return nil
	}

	sectionIdx, ok := findMapKey(c.lines, 0, section, 0)
	if !ok {
		c.lines = append(c.lines, section+":")
		sectionIdx = len(c.lines) - 1
	}
	insertAt := findBlockEnd(c.lines, sectionIdx, 0)
	c.lines = insertLines(c.lines, insertAt, strings.Split(strings.TrimRight(block, "\n"), "\n"))
	return nil
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

func (c *ComposeFile) findSectionEntry(section, key string) (int, bool) {
	sectionIdx, ok := findMapKey(c.lines, 0, section, 0)
	if !ok {
		return 0, false
	}
	return findMapKey(c.lines, sectionIdx+1, key, 2)
}

func (c *ComposeFile) sectionEntryBlock(section, key string) (string, bool) {
	entryIdx, ok := c.findSectionEntry(section, key)
	if !ok {
		return "", false
	}
	end := findBlockEnd(c.lines, entryIdx, 2)
	return strings.Join(c.lines[entryIdx:end], "\n"), true
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

func composeContentLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}
