package memcached_test

import (
	"testing"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/services/memcached"
)

func TestEmbeddedComposeYAMLParses(t *testing.T) {
	t.Parallel()

	defs, err := corecomponent.ParseComposeDefinitions(memcached.ComposeYAML())
	if err != nil {
		t.Fatalf("ParseComposeDefinitions() error = %v", err)
	}
	if _, ok := defs.Definition("services", "memcached"); !ok {
		t.Fatal("expected embedded compose to define memcached service")
	}
}

func TestNewBuildsMemcachedServiceComponent(t *testing.T) {
	t.Parallel()

	component, err := memcached.New(memcached.TargetOptions{
		AppService: "drupal",
		AppDependencies: map[string]any{
			"memcached": map[string]any{"condition": "service_started"},
		},
		AppEnvironment: map[string]string{"CACHE_BACKEND": "memcached"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := component.Name(); got != "memcached" {
		t.Fatalf("Name() = %q, want memcached", got)
	}

	def := component.Definition()
	if def.Name != "memcached" {
		t.Fatalf("Definition().Name = %q, want memcached", def.Name)
	}
	if def.DefaultState != corecomponent.StateOn {
		t.Fatalf("DefaultState = %q, want %q", def.DefaultState, corecomponent.StateOn)
	}
	if !allowsDisposition(def, corecomponent.DispositionEnabled) || !allowsDisposition(def, corecomponent.DispositionDisabled) {
		t.Fatalf("AllowedDispositions = %v, want enabled and disabled", def.AllowedDispositions)
	}
	if allowsDisposition(def, corecomponent.DispositionDistributed) {
		t.Fatalf("AllowedDispositions = %v, did not expect distributed", def.AllowedDispositions)
	}
	if len(def.On.Compose.Rules) == 0 {
		t.Fatal("expected enable compose rules")
	}
}

func allowsDisposition(def corecomponent.Definition, want corecomponent.Disposition) bool {
	for _, disposition := range def.AllowedDispositions {
		if disposition == want {
			return true
		}
	}
	return false
}
