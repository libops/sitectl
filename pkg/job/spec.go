package job

import "strings"

type Spec struct {
	Name        string `yaml:"name"`
	Plugin      string `yaml:"plugin,omitempty"`
	Description string `yaml:"description,omitempty"`
}

func Find(specs []Spec, name string) (Spec, bool) {
	for _, spec := range specs {
		if strings.EqualFold(spec.Name, name) {
			return spec, true
		}
	}
	return Spec{}, false
}
