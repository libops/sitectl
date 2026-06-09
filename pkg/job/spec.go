package job

import "strings"

type Spec struct {
	Name        string `json:"name" yaml:"name"`
	Plugin      string `json:"plugin,omitempty" yaml:"plugin,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

func Find(specs []Spec, name string) (Spec, bool) {
	for _, spec := range specs {
		if strings.EqualFold(spec.Name, name) {
			return spec, true
		}
	}
	return Spec{}, false
}
