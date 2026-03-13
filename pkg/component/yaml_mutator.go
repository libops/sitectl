package component

import (
	"bytes"
	"fmt"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

type YAMLDocument struct {
	node             yaml.Node
	explicitDocStart bool
	explicitMergeTag bool
}

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
		}
		if next.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("set path %q: segment %q is not a mapping", fullPath, segment)
		}
		current = next
	}
	return current, nil
}

func deletePathFromMapping(root *yaml.Node, segments []string, fullPath string) error {
	current := root
	for i, segment := range segments[:len(segments)-1] {
		next := mappingValue(current, segment)
		if next == nil {
			return nil
		}
		if next.Kind != yaml.MappingNode {
			return fmt.Errorf("delete path %q: segment %q is not a mapping", fullPath, segments[i])
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
