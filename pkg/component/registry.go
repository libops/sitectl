package component

import "fmt"

type Registry struct {
	features map[string]Component
}

func NewRegistry(features ...Component) *Registry {
	r := &Registry{
		features: make(map[string]Component, len(features)),
	}
	for _, feature := range features {
		r.MustRegister(feature)
	}
	return r
}

func (r *Registry) Register(feature Component) error {
	name := feature.Name()
	if name == "" {
		return fmt.Errorf("component name cannot be empty")
	}
	if _, exists := r.features[name]; exists {
		return fmt.Errorf("component %q already registered", name)
	}
	r.features[name] = feature
	return nil
}

func (r *Registry) MustRegister(feature Component) {
	if err := r.Register(feature); err != nil {
		panic(err)
	}
}

func (r *Registry) Component(name string) (Component, bool) {
	feature, ok := r.features[name]
	return feature, ok
}

func (r *Registry) Components() []Component {
	out := make([]Component, 0, len(r.features))
	for _, feature := range r.features {
		out = append(out, feature)
	}
	return out
}
