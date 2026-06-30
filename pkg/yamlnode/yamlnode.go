package yamlnode

import (
	"strconv"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// EmptyDocument returns a YAML document with an empty mapping root.
func EmptyDocument() *yaml.Node {
	return &yaml.Node{
		Kind: yaml.DocumentNode,
		Content: []*yaml.Node{{
			Kind: yaml.MappingNode,
			Tag:  "!!map",
		}},
	}
}

// DocumentMapping returns the document's mapping root when present.
func DocumentMapping(doc *yaml.Node) *yaml.Node {
	if doc == nil {
		return nil
	}
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return nil
		}
		if doc.Content[0].Kind == yaml.MappingNode {
			return doc.Content[0]
		}
		return nil
	}
	if doc.Kind == yaml.MappingNode {
		return doc
	}
	return nil
}

// EnsureDocumentMapping ensures doc is a YAML document with a mapping root.
func EnsureDocumentMapping(doc *yaml.Node) *yaml.Node {
	if doc.Kind != yaml.DocumentNode {
		doc.Kind = yaml.DocumentNode
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		doc.Content = []*yaml.Node{{
			Kind: yaml.MappingNode,
			Tag:  "!!map",
		}}
	}
	return doc.Content[0]
}

// MappingValue returns the value for key in a YAML mapping node.
func MappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// EnsureMappingValue ensures key exists as a mapping value below mapping.
func EnsureMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping.Kind != yaml.MappingNode {
		mapping.Kind = yaml.MappingNode
		mapping.Tag = "!!map"
		mapping.Content = nil
	}
	if value := MappingValue(mapping, key); value != nil {
		if value.Kind != yaml.MappingNode {
			value.Kind = yaml.MappingNode
			value.Tag = "!!map"
			value.Content = nil
		}
		return value
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	valueNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	mapping.Content = append(mapping.Content, keyNode, valueNode)
	return valueNode
}

// DeleteMappingKey removes all entries for key from a YAML mapping node.
func DeleteMappingKey(mapping *yaml.Node, key string) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return
	}
	filtered := make([]*yaml.Node, 0, len(mapping.Content))
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			continue
		}
		filtered = append(filtered, mapping.Content[i], mapping.Content[i+1])
	}
	mapping.Content = filtered
}

// SetMappingValue replaces or appends key with value in a YAML mapping node.
func SetMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	if mapping == nil || value == nil {
		return
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

// ScalarValue returns a trimmed scalar node value.
func ScalarValue(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.ScalarNode {
		return ""
	}
	return strings.TrimSpace(node.Value)
}

// StringValues returns scalar values from a scalar or sequence node.
func StringValues(node *yaml.Node) []string {
	switch {
	case node == nil:
		return nil
	case node.Kind == yaml.ScalarNode:
		value := strings.TrimSpace(node.Value)
		if value == "" {
			return nil
		}
		return []string{value}
	case node.Kind == yaml.SequenceNode:
		values := []string{}
		for _, item := range node.Content {
			value := ScalarValue(item)
			if value != "" {
				values = append(values, value)
			}
		}
		return values
	default:
		return nil
	}
}

// StringFieldValues returns shell-like fields from scalar nodes and raw scalar
// values from sequence nodes. It is useful for Docker Compose command fields,
// which can be a block/scalar command string or an argv-style sequence.
func StringFieldValues(node *yaml.Node) []string {
	if node == nil {
		return nil
	}
	switch node.Kind {
	case yaml.ScalarNode:
		return strings.Fields(node.Value)
	case yaml.SequenceNode:
		values := []string{}
		for _, item := range node.Content {
			value := ScalarValue(item)
			if value != "" {
				values = append(values, value)
			}
		}
		return values
	default:
		return nil
	}
}

// IntValue parses a scalar integer value, returning zero on absence or parse failure.
func IntValue(node *yaml.Node) int {
	value := ScalarValue(node)
	if value == "" {
		return 0
	}
	out, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return out
}
