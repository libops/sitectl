package component

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteComponentCatalogIncludesDispositionsAndSetFlags(t *testing.T) {
	t.Parallel()

	defs := []Definition{
		{
			Name:                "fcrepo",
			DefaultDisposition:  DispositionEnabled,
			AllowedDispositions: []Disposition{DispositionEnabled, DispositionSuperseded},
			FollowUps: []FollowUpSpec{
				{
					Name:                 "isle-file-system-uri",
					FlagName:             "isle-file-system-uri",
					FlagUsage:            "Filesystem scheme to use when fcrepo is off",
					DefaultValue:         "private",
					AppliesToDisposition: DispositionSuperseded,
					Choices: []Choice{
						{Value: "public", Label: "public"},
						{Value: "private", Label: "private"},
					},
				},
			},
			Guidance: StateGuidance{
				EnabledHelp:    "Keep Fedora-backed storage.",
				SupersededHelp: "Use another storage approach.",
			},
			Behavior: Behavior{
				Enable: TransitionBehavior{Summary: "Enable Fedora."},
				Disable: TransitionBehavior{
					DataMigration: DataMigrationHard,
					Summary:       "Disable Fedora.",
				},
			},
		},
	}

	var out bytes.Buffer
	if err := WriteComponentCatalog(&out, "ISLE", defs, ""); err != nil {
		t.Fatalf("WriteComponentCatalog() error = %v", err)
	}

	rendered := out.String()
	for _, want := range []string{
		"ISLE components",
		"fcrepo",
		"enabled, superseded",
		"--isle-file-system-uri",
		"when superseded",
		"choices: public, private",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected catalog to contain %q:\n%s", want, rendered)
		}
	}
}

func TestWriteComponentCatalogRejectsUnknownFilter(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	err := WriteComponentCatalog(&out, "ISLE", []Definition{{Name: "fcrepo"}}, "blazegraph")
	if err == nil {
		t.Fatal("expected unknown component error")
	}
}
