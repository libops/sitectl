package component

import "fmt"

type RepoSource struct {
	Repo string
	Ref  string
	Path string
}

type RuleOp string

const (
	OpSet     RuleOp = "set"
	OpDelete  RuleOp = "delete"
	OpRestore RuleOp = "restore"
	OpReplace RuleOp = "replace"
)

type YAMLRule struct {
	Files       []string
	Exclude     []string
	SourceFiles []string
	Op          RuleOp
	Path        string
	Value       any
	Old         any
}

type YAMLStateSpec struct {
	Canonical []RepoSource
	Rules     []YAMLRule
}

func ParseStateOverrides(values map[string]string) (map[string]State, error) {
	out := make(map[string]State, len(values))
	for name, value := range values {
		state, err := ParseState(value)
		if err != nil {
			return nil, fmt.Errorf("parse component %q state: %w", name, err)
		}
		out[name] = state
	}
	return out, nil
}
