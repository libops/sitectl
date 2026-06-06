package component

import (
	"bytes"
	"fmt"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// YAMLDocument is an editable YAML document that preserves selected formatting
// details needed by compose files.
type YAMLDocument struct {
	node             yaml.Node
	explicitDocStart bool
	explicitMergeTag bool
}

// LoadYAMLDocument parses YAML into a mutable document.
func LoadYAMLDocument(data []byte) (*YAMLDocument, error) {
	doc := &YAMLDocument{
		explicitDocStart: hasExplicitDocumentStart(data),
		explicitMergeTag: bytes.Contains(data, []byte("!!merge")),
	}
	if len(bytes.TrimSpace(data)) == 0 {
		doc.node = yaml.Node{
			Kind: yaml.DocumentNode,
			Content: []*yaml.Node{
				{
					Kind:    yaml.MappingNode,
					Tag:     "!!map",
					Content: []*yaml.Node{},
				},
			},
		}
		return doc, nil
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&doc.node); err != nil {
		return nil, fmt.Errorf("decode yaml document: %w", err)
	}
	if doc.node.Kind != yaml.DocumentNode {
		original := doc.node
		doc.node = yaml.Node{
			Kind:    yaml.DocumentNode,
			Content: []*yaml.Node{&original},
		}
	}
	if len(doc.node.Content) == 0 {
		doc.node.Content = []*yaml.Node{{
			Kind:    yaml.MappingNode,
			Tag:     "!!map",
			Content: []*yaml.Node{},
		}}
	}
	return doc, nil
}

// Bytes marshals the YAML document back to bytes.
func (d *YAMLDocument) Bytes() ([]byte, error) {
	if !d.explicitMergeTag {
		stripImplicitMergeTags(&d.node)
	}

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(&d.node); err != nil {
		_ = encoder.Close()
		return nil, fmt.Errorf("marshal yaml document: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close yaml encoder: %w", err)
	}

	out := buf.Bytes()
	if d.explicitDocStart && !bytes.HasPrefix(out, []byte("---\n")) {
		out = append([]byte("---\n"), out...)
	}
	return out, nil
}

// DeletePath removes a dotted path from the YAML mapping when present.
func (d *YAMLDocument) DeletePath(path string) error {
	segments, err := parseYAMLPath(path)
	if err != nil {
		return err
	}
	if len(segments) == 0 {
		return fmt.Errorf("delete path %q: root deletion is not supported", path)
	}

	root := d.mappingRoot()
	if root == nil {
		return nil
	}
	return deletePathFromMapping(root, segments, path)
}

// HasPath reports whether a dotted YAML path exists.
func (d *YAMLDocument) HasPath(path string) (bool, error) {
	segments, err := parseYAMLPath(path)
	if err != nil {
		return false, err
	}
	if len(segments) == 0 {
		return d.mappingRoot() != nil, nil
	}

	current := d.mappingRoot()
	if current == nil {
		return false, nil
	}
	for i, segment := range segments {
		next := mappingValue(current, segment)
		if next == nil {
			return false, nil
		}
		if i == len(segments)-1 {
			return true, nil
		}
		if next.Kind != yaml.MappingNode {
			return false, nil
		}
		current = next
	}
	return true, nil
}

// SetString writes a string scalar at a dotted YAML path.
func (d *YAMLDocument) SetString(path, value string) error {
	segments, err := parseYAMLPath(path)
	if err != nil {
		return err
	}
	if len(segments) == 0 {
		return fmt.Errorf("set path %q: root assignment is not supported", path)
	}

	root := d.ensureMappingRoot()
	target, err := ensurePath(root, segments[:len(segments)-1], path)
	if err != nil {
		return err
	}

	key := segments[len(segments)-1]
	existing := mappingValue(target, key)
	style := yaml.DoubleQuotedStyle
	if existing != nil {
		style = existing.Style
		if style == 0 {
			style = yaml.DoubleQuotedStyle
		}
	}

	valueNode := &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   "!!str",
		Value: value,
		Style: style,
	}
	setMappingValue(target, key, valueNode)
	return nil
}

// SetValue writes an arbitrary YAML value at a dotted path.
func (d *YAMLDocument) SetValue(path string, value any) error {
	segments, err := parseYAMLPath(path)
	if err != nil {
		return err
	}
	if len(segments) == 0 {
		return fmt.Errorf("set path %q: root assignment is not supported", path)
	}

	root := d.ensureMappingRoot()
	target, err := ensurePath(root, segments[:len(segments)-1], path)
	if err != nil {
		return err
	}

	valueNode, err := yamlNodeForValue(value)
	if err != nil {
		return fmt.Errorf("set path %q: %w", path, err)
	}
	setMappingValue(target, segments[len(segments)-1], valueNode)
	return nil
}

// AppendUniqueString appends value to a string sequence at path when absent.
func (d *YAMLDocument) AppendUniqueString(path, value string) error {
	segments, err := parseYAMLPath(path)
	if err != nil {
		return err
	}
	if len(segments) == 0 {
		return fmt.Errorf("append path %q: root assignment is not supported", path)
	}

	root := d.ensureMappingRoot()
	parent, err := ensurePath(root, segments[:len(segments)-1], path)
	if err != nil {
		return err
	}
	key := segments[len(segments)-1]
	target := mappingValue(parent, key)
	if target == nil {
		target = &yaml.Node{
			Kind:    yaml.SequenceNode,
			Tag:     "!!seq",
			Content: []*yaml.Node{},
		}
		setMappingValue(parent, key, target)
	}
	if target.Kind == yaml.ScalarNode {
		if target.Value == value {
			return nil
		}
		target = &yaml.Node{
			Kind: yaml.SequenceNode,
			Tag:  "!!seq",
			Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: target.Value},
			},
		}
		setMappingValue(parent, key, target)
	}
	if target.Kind != yaml.SequenceNode {
		return fmt.Errorf("append path %q: target is not a sequence", path)
	}
	for _, child := range target.Content {
		if child.Kind == yaml.ScalarNode && child.Value == value {
			return nil
		}
	}
	target.Content = append(target.Content, &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   "!!str",
		Value: value,
	})
	return nil
}

// RemoveString removes value from a string sequence or matching scalar at path.
func (d *YAMLDocument) RemoveString(path, value string) error {
	segments, err := parseYAMLPath(path)
	if err != nil {
		return err
	}
	if len(segments) == 0 {
		return fmt.Errorf("remove path %q: root deletion is not supported", path)
	}

	root := d.mappingRoot()
	if root == nil {
		return nil
	}
	parent := root
	for _, segment := range segments[:len(segments)-1] {
		next := mappingValue(parent, segment)
		if next == nil || next.Kind != yaml.MappingNode {
			return nil
		}
		parent = next
	}
	key := segments[len(segments)-1]
	target := mappingValue(parent, key)
	if target == nil {
		return nil
	}
	if target.Kind == yaml.ScalarNode {
		if target.Value == value {
			deleteMappingValue(parent, key)
		}
		return nil
	}
	if target.Kind != yaml.SequenceNode {
		return nil
	}
	filtered := make([]*yaml.Node, 0, len(target.Content))
	for _, child := range target.Content {
		if child.Kind == yaml.ScalarNode && child.Value == value {
			continue
		}
		filtered = append(filtered, child)
	}
	if len(filtered) == 0 {
		deleteMappingValue(parent, key)
		return nil
	}
	target.Content = filtered
	return nil
}

// RemoveMatchingString removes string values matching match from a scalar or
// sequence at path. It returns true when the document changed.
func (d *YAMLDocument) RemoveMatchingString(path string, match func(string) bool) (bool, error) {
	if match == nil {
		return false, fmt.Errorf("remove path %q: nil string matcher", path)
	}
	segments, err := parseYAMLPath(path)
	if err != nil {
		return false, err
	}
	if len(segments) == 0 {
		return false, fmt.Errorf("remove path %q: root deletion is not supported", path)
	}

	root := d.mappingRoot()
	if root == nil {
		return false, nil
	}
	parent := root
	for _, segment := range segments[:len(segments)-1] {
		next := mappingValue(parent, segment)
		if next == nil || next.Kind != yaml.MappingNode {
			return false, nil
		}
		parent = next
	}
	key := segments[len(segments)-1]
	target := mappingValue(parent, key)
	if target == nil {
		return false, nil
	}
	if target.Kind == yaml.ScalarNode {
		if match(target.Value) {
			deleteMappingValue(parent, key)
			return true, nil
		}
		return false, nil
	}
	if target.Kind != yaml.SequenceNode {
		return false, nil
	}
	filtered := make([]*yaml.Node, 0, len(target.Content))
	changed := false
	for _, child := range target.Content {
		if child.Kind == yaml.ScalarNode && match(child.Value) {
			changed = true
			continue
		}
		filtered = append(filtered, child)
	}
	if !changed {
		return false, nil
	}
	if len(filtered) == 0 {
		deleteMappingValue(parent, key)
		return true, nil
	}
	target.Content = filtered
	return true, nil
}

func yamlNodeForValue(value any) (*yaml.Node, error) {
	data, err := yaml.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal value: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("unmarshal value node: %w", err)
	}
	if len(doc.Content) == 0 {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}, nil
	}
	return doc.Content[0], nil
}

func parseYAMLPath(path string) ([]string, error) {
	if path == "" || path == "." {
		return nil, nil
	}
	if !strings.HasPrefix(path, ".") {
		return nil, fmt.Errorf("invalid yaml path %q", path)
	}
	trimmed := strings.TrimPrefix(path, ".")
	if trimmed == "" {
		return nil, nil
	}
	parts := strings.Split(trimmed, ".")
	for _, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("invalid yaml path %q", path)
		}
		if strings.ContainsAny(part, "[]*") {
			return nil, fmt.Errorf("unsupported yaml path segment %q in %q", part, path)
		}
	}
	return parts, nil
}

func hasExplicitDocumentStart(data []byte) bool {
	trimmed := bytes.TrimLeft(data, "\r\n\t ")
	return bytes.HasPrefix(trimmed, []byte("---"))
}

func (d *YAMLDocument) mappingRoot() *yaml.Node {
	if len(d.node.Content) == 0 {
		return nil
	}
	root := d.node.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	return root
}

func (d *YAMLDocument) ensureMappingRoot() *yaml.Node {
	if len(d.node.Content) == 0 {
		d.node.Content = []*yaml.Node{{
			Kind:    yaml.MappingNode,
			Tag:     "!!map",
			Content: []*yaml.Node{},
		}}
	}
	root := d.node.Content[0]
	if root.Kind != yaml.MappingNode {
		root.Kind = yaml.MappingNode
		root.Tag = "!!map"
		root.Content = []*yaml.Node{}
	}
	return root
}

func ensurePath(root *yaml.Node, segments []string, fullPath string) (*yaml.Node, error) {
	current := root
	for _, segment := range segments {
		next := mappingValue(current, segment)
		if next == nil {
			next = &yaml.Node{
				Kind:    yaml.MappingNode,
				Tag:     "!!map",
				Content: []*yaml.Node{},
			}
			setMappingValue(current, segment, next)
		} else if next.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("set path %q: segment %q is not a mapping", fullPath, segment)
		}
		current = next
	}
	return current, nil
}

func deletePathFromMapping(root *yaml.Node, segments []string, fullPath string) error {
	current := root
	for _, segment := range segments[:len(segments)-1] {
		next := mappingValue(current, segment)
		if next == nil {
			return nil
		}
		if next.Kind != yaml.MappingNode {
			return nil
		}
		current = next
	}
	deleteMappingValue(current, segments[len(segments)-1])
	return nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func setMappingValue(node *yaml.Node, key string, value *yaml.Node) {
	if node.Kind != yaml.MappingNode {
		node.Kind = yaml.MappingNode
		node.Tag = "!!map"
		node.Content = []*yaml.Node{}
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1] = value
			return
		}
	}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}

func deleteMappingValue(node *yaml.Node, key string) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}
	filtered := make([]*yaml.Node, 0, len(node.Content))
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			continue
		}
		filtered = append(filtered, node.Content[i], node.Content[i+1])
	}
	node.Content = filtered
}

func stripImplicitMergeTags(node *yaml.Node) {
	if node == nil {
		return
	}
	if node.Kind == yaml.ScalarNode && node.Value == "<<" && node.Tag == "!!merge" {
		node.Tag = ""
	}
	for _, child := range node.Content {
		stripImplicitMergeTags(child)
	}
}
