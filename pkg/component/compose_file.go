package component

import (
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"gopkg.in/yaml.v3"
)

type ComposeFile struct {
	path  string
	lines []string
	ctx   *config.Context
}

func LoadComposeFile(path string) (*ComposeFile, error) {
	return LoadComposeFileForContext(nil, path)
}

func LoadComposeFileForContext(ctx *config.Context, path string) (*ComposeFile, error) {
	if ctx != nil && ctx.DockerHostType == config.ContextRemote {
		data, err := ctx.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read compose file: %w", err)
		}
		return &ComposeFile{
			path:  path,
			lines: strings.Split(string(data), "\n"),
			ctx:   ctx,
		}, nil
	}
	data, err := os.ReadFile(path) // #nosec G304 -- compose file path is an explicit project configuration path.
	if err != nil {
		return nil, fmt.Errorf("read compose file: %w", err)
	}
	return &ComposeFile{
		path:  path,
		lines: strings.Split(string(data), "\n"),
	}, nil
}

func LoadComposeFileOptional(path string) (*ComposeFile, error) {
	return LoadComposeFileOptionalForContext(nil, path)
}

func LoadComposeFileOptionalForContext(ctx *config.Context, path string) (*ComposeFile, error) {
	if ctx != nil && ctx.DockerHostType == config.ContextRemote {
		exists, err := ctx.FileExists(path)
		if err != nil {
			return nil, fmt.Errorf("check compose file: %w", err)
		}
		if !exists {
			return &ComposeFile{
				path: path,
				ctx:  ctx,
			}, nil
		}
		data, err := ctx.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read compose file: %w", err)
		}
		return &ComposeFile{
			path:  path,
			lines: strings.Split(string(data), "\n"),
			ctx:   ctx,
		}, nil
	}
	data, err := os.ReadFile(path) // #nosec G304 -- compose file path is an explicit project configuration path.
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
		if c.ctx != nil {
			return c.ctx.RemoveFile(c.path)
		}
		if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if c.ctx != nil {
		return c.ctx.WriteFile(c.path, []byte(strings.Join(c.lines, "\n")))
	}
	return os.WriteFile(c.path, []byte(strings.Join(c.lines, "\n")), 0o600)
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
	insertAt := insertionIndexBeforeTrailingBlanks(c.lines, findBlockEnd(c.lines, sectionIdx, 0))
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

// AppendUniqueServiceString appends value to a service string list or block
// scalar key while preserving the surrounding compose file text.
func (c *ComposeFile) AppendUniqueServiceString(service, key, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	serviceIdx, ok := c.findService(service)
	if !ok {
		return fmt.Errorf("service %q not found in compose file", service)
	}
	keyIdx, ok := findMapKey(c.lines, serviceIdx+1, key, 4)
	if !ok {
		insertAt := findBlockEnd(c.lines, serviceIdx, 2)
		c.lines = insertLines(c.lines, insertAt, []string{
			"    " + key + ":",
			"      - " + value,
		})
		return nil
	}

	keyLine := strings.TrimSpace(c.lines[keyIdx])
	if isBlockScalarKeyLine(keyLine, key) {
		end := findBlockEnd(c.lines, keyIdx, 4)
		for _, line := range c.lines[keyIdx+1 : end] {
			if strings.TrimSpace(line) == value {
				return nil
			}
		}
		insertAt := insertionIndexBeforeTrailingBlanks(c.lines, end)
		c.lines = insertLines(c.lines, insertAt, []string{"      " + value})
		return nil
	}
	if keyLine == key+":" {
		end := findBlockEnd(c.lines, keyIdx, 4)
		for _, line := range c.lines[keyIdx+1 : end] {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "- ") {
				if trimmed == "" {
					continue
				}
				return fmt.Errorf("service %q key %q is not a string sequence", service, key)
			}
			if strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")) == value {
				return nil
			}
		}
		insertAt := insertionIndexBeforeTrailingBlanks(c.lines, end)
		c.lines = insertLines(c.lines, insertAt, []string{"      - " + value})
		return nil
	}

	prefix := key + ":"
	if strings.HasPrefix(keyLine, prefix) {
		existing := strings.TrimSpace(strings.TrimPrefix(keyLine, prefix))
		if existing == value {
			return nil
		}
		replacement := []string{"    " + key + ":"}
		if existing != "" && existing != "{}" && existing != "[]" {
			replacement = append(replacement, "      - "+existing)
		}
		replacement = append(replacement, "      - "+value)
		end := findBlockEnd(c.lines, keyIdx, 4)
		c.lines = append(c.lines[:keyIdx], append(replacement, c.lines[end:]...)...)
		return nil
	}
	return fmt.Errorf("service %q key %q is not a string sequence", service, key)
}

// RemoveServiceString removes value from a service string list or block scalar
// key while preserving the surrounding compose file text.
func (c *ComposeFile) RemoveServiceString(service, key, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return c.removeServiceStrings(service, key, func(candidate string) bool {
		return strings.TrimSpace(candidate) == value
	})
}

func (c *ComposeFile) RemoveServiceStringsByPrefix(service, key, prefix string) error {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil
	}
	return c.removeServiceStrings(service, key, func(candidate string) bool {
		return strings.HasPrefix(strings.TrimSpace(candidate), prefix)
	})
}

// RemoveServiceVolumesBySource removes short- and long-syntax service volume
// entries whose source exactly matches sourcePath while preserving other Compose
// text and list entries.
func (c *ComposeFile) RemoveServiceVolumesBySource(service, sourcePath string) error {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return nil
	}
	serviceLocation, ok, err := c.flexibleServiceLocation(service, false)
	if err != nil || !ok {
		return err
	}
	volumesLocation, ok := c.flexibleMappingChild(serviceLocation, "volumes")
	if !ok || !flexibleMappingLineCanHaveChildren(c.lines[volumesLocation.index], "volumes") {
		return nil
	}

	end := findBlockEnd(c.lines, volumesLocation.index, volumesLocation.indent)
	itemIndent := flexibleDirectChildIndent(c.lines, volumesLocation.index, volumesLocation.indent, volumesLocation.step)
	filtered := make([]string, 0, end-volumesLocation.index-1)
	for index := volumesLocation.index + 1; index < end; {
		if !composeSequenceItemAtIndent(c.lines[index], itemIndent) {
			filtered = append(filtered, c.lines[index])
			index++
			continue
		}

		itemEnd := index + 1
		for itemEnd < end && !composeSequenceItemAtIndent(c.lines[itemEnd], itemIndent) {
			itemEnd++
		}
		if path.Clean(composeVolumeSource(c.lines[index:itemEnd])) != path.Clean(sourcePath) {
			filtered = append(filtered, c.lines[index:itemEnd]...)
		}
		index = itemEnd
	}
	c.lines = append(c.lines[:volumesLocation.index+1], append(filtered, c.lines[end:]...)...)
	return nil
}

func composeSequenceItemAtIndent(line string, indent int) bool {
	if leadingSpaces(line) != indent {
		return false
	}
	trimmed := strings.TrimSpace(line)
	return trimmed == "-" || strings.HasPrefix(trimmed, "- ")
}

func composeVolumeSource(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	indent := leadingSpaces(lines[0])
	fragment := make([]string, 0, len(lines))
	for _, line := range lines {
		if len(line) >= indent {
			line = line[indent:]
		}
		fragment = append(fragment, line)
	}
	var entries []any
	if err := yaml.Unmarshal([]byte(strings.Join(fragment, "\n")), &entries); err != nil || len(entries) != 1 {
		return ""
	}
	switch entry := entries[0].(type) {
	case string:
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) == 2 {
			return strings.TrimSpace(parts[0])
		}
	case map[string]any:
		source, _ := entry["source"].(string)
		return strings.TrimSpace(source)
	}
	return ""
}

func (c *ComposeFile) removeServiceStrings(service, key string, remove func(string) bool) error {
	if remove == nil {
		return nil
	}
	serviceIdx, ok := c.findService(service)
	if !ok {
		return nil
	}
	keyIdx, ok := findMapKey(c.lines, serviceIdx+1, key, 4)
	if !ok {
		return nil
	}

	keyLine := strings.TrimSpace(c.lines[keyIdx])
	if isBlockScalarKeyLine(keyLine, key) {
		end := findBlockEnd(c.lines, keyIdx, 4)
		filtered := make([]string, 0, end-keyIdx)
		filtered = append(filtered, c.lines[keyIdx])
		for _, line := range c.lines[keyIdx+1 : end] {
			if remove(strings.TrimSpace(line)) {
				continue
			}
			filtered = append(filtered, line)
		}
		if len(composeContentLines(filtered[1:])) == 0 {
			c.lines = append(c.lines[:keyIdx], c.lines[end:]...)
			return nil
		}
		c.lines = append(c.lines[:keyIdx], append(filtered, c.lines[end:]...)...)
		return nil
	}
	if keyLine == key+":" {
		end := findBlockEnd(c.lines, keyIdx, 4)
		filtered := make([]string, 0, end-keyIdx)
		filtered = append(filtered, c.lines[keyIdx])
		for _, line := range c.lines[keyIdx+1 : end] {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "- ") && remove(strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))) {
				continue
			}
			filtered = append(filtered, line)
		}
		if len(composeContentLines(filtered[1:])) == 0 {
			c.lines = append(c.lines[:keyIdx], c.lines[end:]...)
			return nil
		}
		c.lines = append(c.lines[:keyIdx], append(filtered, c.lines[end:]...)...)
		return nil
	}

	prefix := key + ":"
	if strings.HasPrefix(keyLine, prefix) && remove(strings.TrimSpace(strings.TrimPrefix(keyLine, prefix))) {
		end := findBlockEnd(c.lines, keyIdx, 4)
		c.lines = append(c.lines[:keyIdx], c.lines[end:]...)
	}
	return nil
}

func (c *ComposeFile) DeleteServiceKey(service, key string) error {
	serviceLocation, ok, err := c.flexibleServiceLocation(service, false)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	c.deleteFlexibleMappingChild(serviceLocation, key)
	return nil
}

func (c *ComposeFile) SetServiceScalar(service, key, value string) error {
	serviceLocation, ok, err := c.flexibleServiceLocation(service, false)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("service %q not found in compose file", service)
	}
	return c.setFlexibleMappingScalar(serviceLocation, key, value)
}

// SetServiceOverrideScalar sets a service scalar while creating missing
// services and preserving all unrelated Compose text.
func (c *ComposeFile) SetServiceOverrideScalar(service, key, value string) error {
	serviceLocation, _, err := c.flexibleServiceLocation(service, true)
	if err != nil {
		return err
	}
	return c.setFlexibleMappingScalar(serviceLocation, key, value)
}

// SetServiceBuildArg sets a nested services.<service>.build.args value without
// reformatting the rest of the Compose file. Scalar build contexts are
// expanded to build.context before args are added.
func (c *ComposeFile) SetServiceBuildArg(service, name, value string) error {
	serviceLocation, _, err := c.flexibleServiceLocation(service, true)
	if err != nil {
		return err
	}
	buildLocation, err := c.ensureFlexibleMappingChild(serviceLocation, "build", true)
	if err != nil {
		return fmt.Errorf("service %q build: %w", service, err)
	}
	argsLocation, err := c.ensureFlexibleMappingChild(buildLocation, "args", false)
	if err != nil {
		return fmt.Errorf("service %q build args: %w", service, err)
	}
	return c.setFlexibleMappingScalar(argsLocation, name, fmt.Sprintf("%q", value))
}

// DeleteServiceBuildArgs removes the build.args mapping while preserving
// build.context, dockerfile, and other service settings.
func (c *ComposeFile) DeleteServiceBuildArgs(service string) error {
	serviceLocation, ok, err := c.flexibleServiceLocation(service, false)
	if err != nil || !ok {
		return err
	}
	buildLocation, ok := c.flexibleMappingChild(serviceLocation, "build")
	if !ok {
		return nil
	}
	if !flexibleMappingLineCanHaveChildren(c.lines[buildLocation.index], "build") {
		return nil
	}
	if !c.deleteFlexibleMappingChild(buildLocation, "args") {
		return nil
	}
	if !c.flexibleMappingHasContent(buildLocation) {
		c.deleteFlexibleMappingChild(serviceLocation, "build")
	}
	return nil
}

// PruneEmptyService removes a service and then services when neither contains
// any YAML values after an override is cleared.
func (c *ComposeFile) PruneEmptyService(service string) error {
	serviceLocation, ok, err := c.flexibleServiceLocation(service, false)
	if err != nil || !ok {
		return err
	}
	if c.flexibleMappingHasContent(serviceLocation) {
		return nil
	}
	serviceEnd := findBlockEnd(c.lines, serviceLocation.index, serviceLocation.indent)
	c.lines = append(c.lines[:serviceLocation.index], c.lines[serviceEnd:]...)

	servicesIndex, ok := findMapKey(c.lines, 0, "services", 0)
	if !ok {
		return nil
	}
	servicesLocation := flexibleMapLocation{index: servicesIndex, indent: 0, step: flexibleChildStep(c.lines, servicesIndex, 0, 2)}
	if c.flexibleMappingHasContent(servicesLocation) {
		return nil
	}
	servicesEnd := findBlockEnd(c.lines, servicesIndex, 0)
	c.lines = append(c.lines[:servicesIndex], c.lines[servicesEnd:]...)
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
	insertAt := insertionIndexBeforeTrailingBlanks(c.lines, findBlockEnd(c.lines, sectionIdx, 0))
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

func isBlockScalarKeyLine(line, key string) bool {
	prefix := key + ":"
	if !strings.HasPrefix(line, prefix) {
		return false
	}
	value := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	return strings.HasPrefix(value, ">") || strings.HasPrefix(value, "|")
}

func (c *ComposeFile) sectionEntryBlock(section, key string) (string, bool) {
	entryIdx, ok := c.findSectionEntry(section, key)
	if !ok {
		return "", false
	}
	end := findBlockEnd(c.lines, entryIdx, 2)
	return strings.Join(c.lines[entryIdx:end], "\n"), true
}

type flexibleMapLocation struct {
	index  int
	indent int
	step   int
	key    string
}

func (c *ComposeFile) flexibleServiceLocation(service string, create bool) (flexibleMapLocation, bool, error) {
	service = strings.TrimSpace(service)
	if service == "" {
		return flexibleMapLocation{}, false, fmt.Errorf("service name is empty")
	}
	servicesIndex, ok := findMapKey(c.lines, 0, "services", 0)
	if !ok {
		if !create {
			return flexibleMapLocation{}, false, nil
		}
		c.lines = append(c.lines, "services:")
		servicesIndex = len(c.lines) - 1
	}
	servicesLocation := flexibleMapLocation{
		index:  servicesIndex,
		indent: 0,
		step:   flexibleChildStep(c.lines, servicesIndex, 0, 2),
		key:    "services",
	}
	if _, err := c.normalizeFlexibleMappingParent(servicesLocation, false); err != nil {
		return flexibleMapLocation{}, false, err
	}

	serviceIndent := flexibleDirectChildIndent(c.lines, servicesIndex, 0, servicesLocation.step)
	serviceIndex, ok := findMapKey(c.lines, servicesIndex+1, service, serviceIndent)
	if !ok {
		if !create {
			return flexibleMapLocation{}, false, nil
		}
		insertAt := insertionIndexBeforeTrailingBlanks(c.lines, findBlockEnd(c.lines, servicesIndex, 0))
		c.lines = insertLines(c.lines, insertAt, []string{strings.Repeat(" ", serviceIndent) + service + ":"})
		serviceIndex = insertAt
	}
	location := flexibleMapLocation{
		index:  serviceIndex,
		indent: serviceIndent,
		step:   flexibleChildStep(c.lines, serviceIndex, serviceIndent, servicesLocation.step),
		key:    service,
	}
	if _, err := c.normalizeFlexibleMappingParent(location, false); err != nil {
		return flexibleMapLocation{}, false, err
	}
	return location, true, nil
}

func (c *ComposeFile) ensureFlexibleMappingChild(parent flexibleMapLocation, key string, allowScalarContext bool) (flexibleMapLocation, error) {
	if _, err := c.normalizeFlexibleMappingParent(parent, false); err != nil {
		return flexibleMapLocation{}, err
	}
	if location, ok := c.flexibleMappingChild(parent, key); ok {
		scalar, err := c.normalizeFlexibleMappingParent(location, allowScalarContext)
		if err != nil {
			return flexibleMapLocation{}, err
		}
		if scalar != "" {
			contextLine := strings.Repeat(" ", location.indent+location.step) + "context: " + scalar
			c.lines = insertLines(c.lines, location.index+1, []string{contextLine})
		}
		location.step = flexibleChildStep(c.lines, location.index, location.indent, location.step)
		return location, nil
	}

	childIndent := flexibleDirectChildIndent(c.lines, parent.index, parent.indent, parent.step)
	insertAt := insertionIndexBeforeTrailingBlanks(c.lines, findBlockEnd(c.lines, parent.index, parent.indent))
	c.lines = insertLines(c.lines, insertAt, []string{strings.Repeat(" ", childIndent) + key + ":"})
	return flexibleMapLocation{index: insertAt, indent: childIndent, step: parent.step, key: key}, nil
}

func (c *ComposeFile) flexibleMappingChild(parent flexibleMapLocation, key string) (flexibleMapLocation, bool) {
	childIndent := flexibleDirectChildIndent(c.lines, parent.index, parent.indent, parent.step)
	childIndex, ok := findMapKey(c.lines, parent.index+1, key, childIndent)
	if !ok {
		return flexibleMapLocation{}, false
	}
	return flexibleMapLocation{
		index:  childIndex,
		indent: childIndent,
		step:   flexibleChildStep(c.lines, childIndex, childIndent, parent.step),
		key:    key,
	}, true
}

func (c *ComposeFile) setFlexibleMappingScalar(parent flexibleMapLocation, key, value string) error {
	if _, err := c.normalizeFlexibleMappingParent(parent, false); err != nil {
		return err
	}
	childIndent := flexibleDirectChildIndent(c.lines, parent.index, parent.indent, parent.step)
	line := strings.Repeat(" ", childIndent) + key + ": " + value
	if childIndex, ok := findMapKey(c.lines, parent.index+1, key, childIndent); ok {
		_, comment, _ := flexibleMappingLineParts(c.lines[childIndex], key)
		line += comment
		end := findBlockEnd(c.lines, childIndex, childIndent)
		c.lines = append(c.lines[:childIndex], append([]string{line}, c.lines[end:]...)...)
		return nil
	}
	insertAt := insertionIndexBeforeTrailingBlanks(c.lines, findBlockEnd(c.lines, parent.index, parent.indent))
	c.lines = insertLines(c.lines, insertAt, []string{line})
	return nil
}

func (c *ComposeFile) deleteFlexibleMappingChild(parent flexibleMapLocation, key string) bool {
	location, ok := c.flexibleMappingChild(parent, key)
	if !ok {
		return false
	}
	end := findBlockEnd(c.lines, location.index, location.indent)
	c.lines = append(c.lines[:location.index], c.lines[end:]...)
	return true
}

func (c *ComposeFile) flexibleMappingHasContent(location flexibleMapLocation) bool {
	end := findBlockEnd(c.lines, location.index, location.indent)
	for _, line := range c.lines[location.index+1 : end] {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			return true
		}
	}
	return false
}

func (c *ComposeFile) normalizeFlexibleMappingParent(location flexibleMapLocation, allowScalarContext bool) (string, error) {
	value, comment, ok := flexibleMappingLineParts(c.lines[location.index], location.key)
	if !ok {
		return "", fmt.Errorf("mapping key %q is malformed", location.key)
	}
	switch {
	case value == "":
		return "", nil
	case value == "{}" || value == "{ }":
		c.lines[location.index] = strings.Repeat(" ", location.indent) + location.key + ":" + comment
		return "", nil
	case strings.HasPrefix(value, "&") || strings.HasPrefix(value, "!"):
		return "", nil
	case allowScalarContext:
		c.lines[location.index] = strings.Repeat(" ", location.indent) + location.key + ":" + comment
		return value, nil
	default:
		return "", fmt.Errorf("mapping key %q has scalar value %q", location.key, value)
	}
}

func flexibleMappingLineCanHaveChildren(line, key string) bool {
	value, _, ok := flexibleMappingLineParts(line, key)
	return ok && (value == "" || value == "{}" || value == "{ }" || strings.HasPrefix(value, "&") || strings.HasPrefix(value, "!"))
}

func flexibleMappingLineParts(line, key string) (string, string, bool) {
	trimmed := strings.TrimLeft(line, " ")
	prefix := key + ":"
	if !strings.HasPrefix(trimmed, prefix) {
		return "", "", false
	}
	value, comment := splitFlexibleInlineComment(strings.TrimPrefix(trimmed, prefix))
	return strings.TrimSpace(value), comment, true
}

func splitFlexibleInlineComment(value string) (string, string) {
	inSingle := false
	inDouble := false
	escaped := false
	for index := 0; index < len(value); index++ {
		character := value[index]
		if inDouble && escaped {
			escaped = false
			continue
		}
		if inDouble && character == '\\' {
			escaped = true
			continue
		}
		switch character {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if inSingle || inDouble || (index > 0 && value[index-1] != ' ' && value[index-1] != '\t') {
				continue
			}
			commentStart := index
			for commentStart > 0 && (value[commentStart-1] == ' ' || value[commentStart-1] == '\t') {
				commentStart--
			}
			return value[:commentStart], value[commentStart:]
		}
	}
	return value, ""
}

func flexibleDirectChildIndent(lines []string, parentIndex, parentIndent, fallbackStep int) int {
	end := findBlockEnd(lines, parentIndex, parentIndent)
	for _, line := range lines[parentIndex+1 : end] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := leadingSpaces(line)
		if indent > parentIndent {
			return indent
		}
	}
	if fallbackStep <= 0 {
		fallbackStep = 2
	}
	return parentIndent + fallbackStep
}

func flexibleChildStep(lines []string, parentIndex, parentIndent, fallbackStep int) int {
	childIndent := flexibleDirectChildIndent(lines, parentIndex, parentIndent, fallbackStep)
	if step := childIndent - parentIndent; step > 0 {
		return step
	}
	if fallbackStep > 0 {
		return fallbackStep
	}
	return 2
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

func insertionIndexBeforeTrailingBlanks(lines []string, index int) int {
	for index > 0 && strings.TrimSpace(lines[index-1]) == "" {
		index--
	}
	return index
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
