package component

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestResolveCreateStatesPromptsForMissingFlags(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{Use: "create"}
	AddCreateFlags(cmd,
		CreateOption{Name: "fcrepo", Default: StateOn, Guidance: StateGuidance{Question: "fcrepo?", DefaultState: StateOn}},
		CreateOption{Name: "blazegraph", Default: StateOn, Guidance: StateGuidance{Question: "blazegraph?", DefaultState: StateOn}},
	)

	var prompts [][]string
	inputs := []string{"off", "on"}
	states, err := ResolveCreateStates(cmd, func(question ...string) (string, error) {
		prompts = append(prompts, question)
		value := inputs[0]
		inputs = inputs[1:]
		return value, nil
	},
		CreateOption{Name: "fcrepo", Default: StateOn, Guidance: StateGuidance{Question: "fcrepo?", DefaultState: StateOn}},
		CreateOption{Name: "blazegraph", Default: StateOn, Guidance: StateGuidance{Question: "blazegraph?", DefaultState: StateOn}},
	)
	if err != nil {
		t.Fatalf("ResolveCreateStates() error = %v", err)
	}

	if len(prompts) != 2 {
		t.Fatalf("expected 2 prompts, got %d", len(prompts))
	}
	if states["fcrepo"] != StateOff {
		t.Fatalf("expected fcrepo off, got %q", states["fcrepo"])
	}
	if states["blazegraph"] != StateOn {
		t.Fatalf("expected blazegraph on, got %q", states["blazegraph"])
	}
}

func TestResolveCreateStatesUsesExplicitFlags(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{Use: "create"}
	AddCreateFlags(cmd,
		CreateOption{Name: "fcrepo", Default: StateOn},
		CreateOption{Name: "blazegraph", Default: StateOn},
	)
	_ = cmd.Flags().Set("fcrepo", "off")
	_ = cmd.Flags().Set("blazegraph", "on")

	states, err := ResolveCreateStates(cmd, func(question ...string) (string, error) {
		t.Fatal("did not expect prompt")
		return "", nil
	},
		CreateOption{Name: "fcrepo", Default: StateOn},
		CreateOption{Name: "blazegraph", Default: StateOn},
	)
	if err != nil {
		t.Fatalf("ResolveCreateStates() error = %v", err)
	}

	if states["fcrepo"] != StateOff || states["blazegraph"] != StateOn {
		t.Fatalf("unexpected states %+v", states)
	}
}

func TestResolveCreateStatesRejectsInvalidFlagValue(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{Use: "create"}
	AddCreateFlags(cmd, CreateOption{Name: "fcrepo", Default: StateOn})
	_ = cmd.Flags().Set("fcrepo", "maybe")

	if _, err := ResolveCreateStates(cmd, nil, CreateOption{Name: "fcrepo", Default: StateOn}); err == nil {
		t.Fatal("expected invalid flag error")
	}
}
