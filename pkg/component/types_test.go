package component

import "testing"

func TestParseStateOverrides(t *testing.T) {
	t.Parallel()

	overrides, err := ParseStateOverrides(map[string]string{
		"fcrepo":     "off",
		"blazegraph": "ON",
	})
	if err != nil {
		t.Fatalf("ParseStateOverrides() error = %v", err)
	}

	if overrides["fcrepo"] != StateOff {
		t.Fatalf("expected fcrepo off, got %q", overrides["fcrepo"])
	}
	if overrides["blazegraph"] != StateOn {
		t.Fatalf("expected blazegraph on, got %q", overrides["blazegraph"])
	}
}

func TestParseStateOverridesRejectsInvalidValue(t *testing.T) {
	t.Parallel()

	if _, err := ParseStateOverrides(map[string]string{"fcrepo": "maybe"}); err == nil {
		t.Fatal("expected invalid state error")
	}
}
