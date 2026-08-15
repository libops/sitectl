package component

import (
	"bytes"
	"fmt"
	"os"
	pathpkg "path"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/mikefarah/yq/v4/pkg/yqlib"
)

type ComposeFile struct {
	path        string
	doc         *YAMLDocument
	ctx         *config.Context
	retainEmpty bool
}

func LoadComposeFile(path string) (*ComposeFile, error) {
	return LoadComposeFileForContext(nil, path)
}

func LoadComposeFileForContext(ctx *config.Context, path string) (*ComposeFile, error) {
	var (
		data []byte
		err  error
	)
	if ctx != nil && ctx.DockerHostType == config.ContextRemote {
		data, err = ctx.ReadFile(path)
	} else {
		data, err = os.ReadFile(path) // #nosec G304 -- compose file path is an explicit project configuration path.
	}
	if err != nil {
		return nil, fmt.Errorf("read compose file: %w", err)
	}
	return newComposeFile(ctx, path, data)
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
			return newComposeFile(ctx, path, nil)
		}
		data, err := ctx.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read compose file: %w", err)
		}
		return newComposeFile(ctx, path, data)
	}

	data, err := os.ReadFile(path) // #nosec G304 -- compose file path is an explicit project configuration path.
	if err != nil {
		if os.IsNotExist(err) {
			return newComposeFile(nil, path, nil)
		}
		return nil, fmt.Errorf("read compose file: %w", err)
	}
	return newComposeFile(nil, path, data)
}

func newComposeFile(ctx *config.Context, filePath string, data []byte) (*ComposeFile, error) {
	doc, err := LoadYAMLDocument(data)
	if err != nil {
		return nil, fmt.Errorf("parse compose file: %w", err)
	}
	return &ComposeFile{
		path:        filePath,
		doc:         doc,
		ctx:         ctx,
		retainEmpty: len(bytes.TrimSpace(data)) > 0 && composeEmptyMapping(doc.node),
	}, nil
}

func (c *ComposeFile) Save() error {
	if c == nil || c.doc == nil || composeDocumentEmpty(c.doc.node, c.retainEmpty) {
		if c != nil && c.ctx != nil {
			return c.ctx.RemoveFile(c.path)
		}
		if c == nil {
			return nil
		}
		if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	data, err := c.doc.Bytes()
	if err != nil {
		return fmt.Errorf("encode compose file: %w", err)
	}
	if c.ctx != nil {
		return c.ctx.WriteFile(c.path, data)
	}
	return os.WriteFile(c.path, data, 0o600)
}

func (c *ComposeFile) DeleteService(name string) error {
	return c.deleteSectionEntry("services", name)
}

func (c *ComposeFile) DeleteVolume(name string) error {
	return c.deleteSectionEntry("volumes", name)
}

func (c *ComposeFile) HasService(name string) bool {
	return c.hasPath("services", name)
}

func (c *ComposeFile) HasVolume(name string) bool {
	return c.hasPath("volumes", name)
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

func (c *ComposeFile) SetServiceStringList(service, key string, values []string) error {
	if !c.HasService(service) {
		return fmt.Errorf("service %q not found in compose file", service)
	}
	return c.doc.setYAMLValue([]string{"services", service, key}, values)
}

// AppendUniqueServiceString appends a scalar to a service string sequence or
// block scalar without changing unrelated YAML nodes.
func (c *ComposeFile) AppendUniqueServiceString(service, key, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if !c.HasService(service) {
		return fmt.Errorf("service %q not found in compose file", service)
	}
	candidate, err := composeScalarCandidate(value)
	if err != nil {
		return fmt.Errorf("service %q key %q: %w", service, key, err)
	}
	target := c.pathCandidate("services", service, key)
	if target != nil && target.Kind == yqlib.ScalarNode && target.Style == yqlib.FoldedStyle {
		if !scalarStringContains(target.Value, candidate.Value) {
			target.Value = strings.TrimSpace(target.Value) + " " + candidate.Value
		}
		return nil
	}
	return c.doc.appendUniqueYAMLString([]string{"services", service, key}, candidate.Value)
}

// RemoveServiceString removes a scalar from a service sequence or block scalar.
func (c *ComposeFile) RemoveServiceString(service, key, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	candidate, err := composeScalarCandidate(value)
	if err != nil {
		return fmt.Errorf("service %q key %q: %w", service, key, err)
	}
	if yamlCandidateAtKeys(c.doc.node, "services", service, key) == nil {
		return nil
	}
	return c.doc.RemoveString(yamlPath("services", service, key), candidate.Value)
}

func (c *ComposeFile) RemoveServiceStringsByPrefix(service, key, prefix string) error {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil
	}
	if yamlCandidateAtKeys(c.doc.node, "services", service, key) == nil {
		return nil
	}
	_, err := c.doc.RemoveMatchingString(yamlPath("services", service, key), func(candidate string) bool {
		return strings.HasPrefix(strings.TrimSpace(candidate), prefix)
	})
	return err
}

// RemoveServiceVolumesBySource removes short- and long-syntax volume entries
// whose source is sourcePath.
func (c *ComposeFile) RemoveServiceVolumesBySource(service, sourcePath string) error {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return nil
	}
	target := c.pathCandidate("services", service, "volumes")
	if target == nil || target.Kind != yqlib.SequenceNode {
		return nil
	}
	matches := make([]*yqlib.CandidateNode, 0)
	for _, item := range target.Content {
		if pathpkg.Clean(composeVolumeCandidateSource(item)) == pathpkg.Clean(sourcePath) {
			matches = append(matches, item)
		}
	}
	return deleteYAMLCandidates(matches...)
}

func (c *ComposeFile) DeleteServiceKey(service, key string) error {
	return c.doc.deleteYAMLKeys("services", service, key)
}

func (c *ComposeFile) SetServiceScalar(service, key, value string) error {
	if !c.HasService(service) {
		return fmt.Errorf("service %q not found in compose file", service)
	}
	return c.setRawScalar([]string{"services", service, key}, value)
}

// SetServiceOverrideScalar sets a service scalar while creating missing services.
func (c *ComposeFile) SetServiceOverrideScalar(service, key, value string) error {
	if strings.TrimSpace(service) == "" {
		return fmt.Errorf("service name is empty")
	}
	return c.setRawScalar([]string{"services", service, key}, value)
}

// SetServiceBuildArg sets services.<service>.build.args.<name>. Scalar build
// contexts are expanded to build.context first.
func (c *ComposeFile) SetServiceBuildArg(service, name, value string) error {
	if strings.TrimSpace(service) == "" {
		return fmt.Errorf("service name is empty")
	}
	build := c.pathCandidate("services", service, "build")
	if build != nil && build.Kind == yqlib.ScalarNode && build.Tag != "!!null" {
		context := build.Copy()
		context.LineComment = ""
		build.Key.LineComment = build.LineComment
		build.LineComment = ""
		build.Kind = yqlib.MappingNode
		build.Tag = "!!map"
		build.Value = ""
		build.Style = 0
		build.Alias = nil
		build.Content = nil
		build.AddKeyValueChild(newYAMLStringCandidate("context", 0), context)
	} else if build != nil && build.Kind != yqlib.MappingNode && build.Tag != "!!null" {
		return fmt.Errorf("service %q build is not a mapping", service)
	}
	args := c.pathCandidate("services", service, "build", "args")
	if args != nil && args.Kind != yqlib.MappingNode && args.Tag != "!!null" {
		return fmt.Errorf("service %q build args is not a mapping", service)
	}
	return c.doc.setYAMLString([]string{"services", service, "build", "args", name}, value)
}

// DeleteServiceBuildArgs removes build.args and prunes build when it becomes empty.
func (c *ComposeFile) DeleteServiceBuildArgs(service string) error {
	args := c.pathCandidate("services", service, "build", "args")
	if args == nil {
		return nil
	}
	if err := c.doc.deleteYAMLKeys("services", service, "build", "args"); err != nil {
		return err
	}
	build := c.pathCandidate("services", service, "build")
	if composeEmptyMapping(build) {
		return c.doc.deleteYAMLKeys("services", service, "build")
	}
	return nil
}

// PruneEmptyService removes an empty service and then an empty services section.
func (c *ComposeFile) PruneEmptyService(service string) error {
	node := c.pathCandidate("services", service)
	if node == nil {
		return nil
	}
	if !composeEmptyValue(node) {
		return nil
	}
	return c.DeleteService(service)
}

func (c *ComposeFile) DeleteSectionEntry(section, key string) error {
	return c.deleteSectionEntry(section, key)
}

func (c *ComposeFile) SectionEntryBlock(section, key string) (string, bool) {
	return c.sectionEntryBlock(section, key)
}

func (c *ComposeFile) AddSectionEntryBlock(section, key, block string) error {
	return c.addSectionEntryBlock(section, key, block)
}

func (c *ComposeFile) DeleteServiceEnv(service, key string) error {
	return c.doc.deleteYAMLKeys("services", service, "environment", key)
}

func (c *ComposeFile) SetServiceEnv(service, key, value string) error {
	if !c.HasService(service) {
		return fmt.Errorf("service %q not found in compose file", service)
	}
	return c.doc.setYAMLString([]string{"services", service, "environment", key}, value)
}

func (c *ComposeFile) deleteSectionEntry(section, key string) error {
	if err := c.doc.deleteYAMLKeys(section, key); err != nil {
		return err
	}
	sectionNode := c.pathCandidate(section)
	if composeEmptyMapping(sectionNode) {
		return c.doc.deleteYAMLKeys(section)
	}
	return nil
}

func (c *ComposeFile) sectionEntryBlock(section, key string) (string, bool) {
	_, _, valueNode := composeMappingEntry(c.doc.node, section)
	if valueNode == nil || valueNode.Kind != yqlib.MappingNode {
		return "", false
	}
	_, entryKey, entryValue := composeMappingEntry(valueNode, key)
	if entryValue == nil {
		return "", false
	}
	block, err := composeEncodeEntry(entryKey, entryValue)
	if err != nil {
		return "", false
	}
	return composeIndentBlock(block, 2), true
}

func (c *ComposeFile) addSectionEntryBlock(section, key, block string) error {
	if strings.TrimSpace(block) == "" {
		return fmt.Errorf("section block for %s.%s is empty", section, key)
	}
	if c.hasPath(section, key) {
		return nil
	}
	entryKey, entryValue, err := c.parseEntryBlock(block, key)
	if err != nil {
		return fmt.Errorf("parse %s block for %q: %w", section, key, err)
	}
	sectionNode, err := c.ensureSectionMapping(section)
	if err != nil {
		return err
	}
	composeAddMappingEntry(sectionNode, entryKey, entryValue)
	return nil
}

func (c *ComposeFile) ensureSectionMapping(section string) (*yqlib.CandidateNode, error) {
	node := c.pathCandidate(section)
	if node == nil || (node.Kind == yqlib.ScalarNode && node.Tag == "!!null") {
		if err := c.doc.assignYAMLKeys([]string{section}, newYAMLMappingCandidate()); err != nil {
			return nil, err
		}
		return c.pathCandidate(section), nil
	}
	if node.Kind != yqlib.MappingNode {
		return nil, fmt.Errorf("compose section %q is not a mapping", section)
	}
	return node, nil
}

func (c *ComposeFile) setRawScalar(keys []string, value string) error {
	candidate, err := composeScalarCandidate(value)
	if err != nil {
		return err
	}
	return c.doc.assignYAMLKeys(keys, candidate)
}

func (c *ComposeFile) hasPath(keys ...string) bool {
	return yamlCandidateAtKeys(c.doc.node, keys...) != nil
}

func (c *ComposeFile) pathCandidate(keys ...string) *yqlib.CandidateNode {
	return yamlCandidateAtKeys(c.doc.node, keys...)
}

func composeScalarCandidate(value string) (*yqlib.CandidateNode, error) {
	doc, err := LoadYAMLDocument([]byte("value: " + value + "\n"))
	if err != nil {
		return nil, fmt.Errorf("invalid YAML scalar %q: %w", value, err)
	}
	_, _, candidate := composeMappingEntry(doc.node, "value")
	if candidate == nil || candidate.Kind != yqlib.ScalarNode {
		return nil, fmt.Errorf("value %q is not a YAML scalar", value)
	}
	return candidate.Copy(), nil
}

func composeMappingEntry(mapping *yqlib.CandidateNode, key string) (int, *yqlib.CandidateNode, *yqlib.CandidateNode) {
	if mapping == nil || mapping.Kind != yqlib.MappingNode {
		return -1, nil, nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return index, mapping.Content[index], mapping.Content[index+1]
		}
	}
	return -1, nil, nil
}

func composeVolumeCandidateSource(candidate *yqlib.CandidateNode) string {
	if candidate == nil {
		return ""
	}
	if candidate.Kind == yqlib.ScalarNode {
		parts := strings.SplitN(candidate.Value, ":", 2)
		if len(parts) == 2 {
			return strings.TrimSpace(parts[0])
		}
		return ""
	}
	_, _, source := composeMappingEntry(candidate, "source")
	if source != nil && source.Kind == yqlib.ScalarNode {
		return strings.TrimSpace(source.Value)
	}
	return ""
}

func composeEmptyMapping(node *yqlib.CandidateNode) bool {
	return node != nil && node.Kind == yqlib.MappingNode && len(node.Content) == 0
}

func composeEmptyValue(node *yqlib.CandidateNode) bool {
	return composeEmptyMapping(node) || (node != nil && node.Kind == yqlib.ScalarNode && node.Tag == "!!null")
}

func composeDocumentEmpty(node *yqlib.CandidateNode, retainEmpty bool) bool {
	if node == nil {
		return true
	}
	if composeEmptyMapping(node) {
		return !retainEmpty && node.LeadingContent == "" && node.HeadComment == "" && node.LineComment == "" && node.FootComment == ""
	}
	return node.Kind == yqlib.ScalarNode && node.Tag == "!!null" && strings.TrimSpace(node.Value) == "" &&
		node.LeadingContent == "" && node.HeadComment == "" && node.LineComment == "" && node.FootComment == ""
}

func composeEncodeEntry(key, value *yqlib.CandidateNode) (string, error) {
	root := newYAMLMappingCandidate()
	// AddKeyValueChild clones both nodes, so normalizing merge tags for the
	// standalone block cannot mutate the live compose document.
	root.AddKeyValueChild(key, value)
	stripImplicitMergeTags(root)
	prefs := yamlPreferences()
	prefs.PrintDocSeparators = false
	prefs.UnwrapScalar = false
	var output bytes.Buffer
	if err := yqlib.NewYamlEncoder(prefs).Encode(&output, root); err != nil {
		return "", err
	}
	return strings.TrimRight(output.String(), "\r\n"), nil
}

func composeIndentBlock(block string, spaces int) string {
	prefix := strings.Repeat(" ", spaces)
	lines := strings.Split(block, "\n")
	for index, line := range lines {
		if line != "" {
			lines[index] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}

func (c *ComposeFile) parseEntryBlock(block, key string) (*yqlib.CandidateNode, *yqlib.CandidateNode, error) {
	const wrapperKey = "x-sitectl-entry"
	const anchorWrapperKey = "x-sitectl-anchors"
	normalizedBlock := strings.TrimRight(block, "\r\n")
	if len(normalizedBlock)-len(strings.TrimLeft(normalizedBlock, " ")) < 2 {
		normalizedBlock = composeIndentBlock(normalizedBlock, 2)
	}

	dummyAnchors := []string{}
	seenDummyAnchor := map[string]bool{}
	var parsed *YAMLDocument
	for {
		var input strings.Builder
		if len(dummyAnchors) > 0 {
			input.WriteString(anchorWrapperKey + ":\n")
			for index, anchor := range dummyAnchors {
				_, _ = fmt.Fprintf(&input, "  anchor-%d: &%s {}\n", index, anchor)
			}
		}
		input.WriteString(wrapperKey + ":\n")
		input.WriteString(normalizedBlock)
		input.WriteByte('\n')

		var err error
		parsed, err = LoadYAMLDocument([]byte(input.String()))
		if err == nil {
			break
		}
		anchor := composeUnknownAnchor(err)
		if anchor == "" || seenDummyAnchor[anchor] || len(dummyAnchors) >= 32 {
			return nil, nil, err
		}
		seenDummyAnchor[anchor] = true
		dummyAnchors = append(dummyAnchors, anchor)
	}
	_, _, entries := composeMappingEntry(parsed.node, wrapperKey)
	_, entryKey, entryValue := composeMappingEntry(entries, key)
	if entryValue == nil {
		return nil, nil, fmt.Errorf("block does not define %q", key)
	}
	_, _, anchors := composeMappingEntry(parsed.node, anchorWrapperKey)
	for index := 1; anchors != nil && index < len(anchors.Content); index += 2 {
		dummy := anchors.Content[index]
		if actual := composeAnchor(c.doc.node, dummy.Anchor); actual != nil {
			composeReplaceAlias(entryValue, dummy, actual)
		} else if err := composeDropUnresolvedMergeAlias(entryValue, dummy); err != nil {
			return nil, nil, err
		}
	}
	return entryKey, entryValue, nil
}

func composeUnknownAnchor(err error) string {
	const marker = "unknown anchor '"
	message := err.Error()
	start := strings.Index(message, marker)
	if start < 0 {
		return ""
	}
	name := message[start+len(marker):]
	end := strings.IndexByte(name, '\'')
	if end <= 0 {
		return ""
	}
	name = name[:end]
	if strings.Trim(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
		return ""
	}
	return name
}

func composeAnchor(node *yqlib.CandidateNode, name string) *yqlib.CandidateNode {
	if node == nil || name == "" {
		return nil
	}
	if node.Anchor == name {
		return node
	}
	for _, child := range node.Content {
		if anchor := composeAnchor(child, name); anchor != nil {
			return anchor
		}
	}
	return nil
}

func composeReplaceAlias(node, oldAnchor, newAnchor *yqlib.CandidateNode) {
	if node == nil {
		return
	}
	if node.Alias == oldAnchor {
		node.Alias = newAnchor
	}
	for _, child := range node.Content {
		composeReplaceAlias(child, oldAnchor, newAnchor)
	}
}

func composeDropUnresolvedMergeAlias(node, anchor *yqlib.CandidateNode) error {
	var deletions []*yqlib.CandidateNode
	var visit func(*yqlib.CandidateNode) error
	visit = func(candidate *yqlib.CandidateNode) error {
		if candidate == nil {
			return nil
		}
		if candidate.Alias == anchor {
			mergeValue := candidate.Key != nil && candidate.Key.Value == "<<"
			mergeSequence := candidate.Parent != nil && candidate.Parent.Key != nil && candidate.Parent.Key.Value == "<<"
			if !mergeValue && !mergeSequence {
				return fmt.Errorf("block references unknown anchor %q outside a merge key", anchor.Anchor)
			}
			if mergeSequence && len(candidate.Parent.Content) == 1 {
				deletions = append(deletions, candidate.Parent)
			} else {
				deletions = append(deletions, candidate)
			}
			return nil
		}
		for _, child := range candidate.Content {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(node); err != nil {
		return err
	}
	return deleteYAMLCandidates(deletions...)
}

func composeAddMappingEntry(mapping, key, value *yqlib.CandidateNode) {
	key.SetParent(mapping)
	key.IsMapKey = true
	value.SetParent(mapping)
	value.IsMapKey = false
	value.Key = key
	mapping.Content = append(mapping.Content, key, value)
}
