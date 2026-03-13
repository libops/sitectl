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

func TestDependenciesDrupalModulesForEnable(t *testing.T) {
	t.Parallel()

	deps := Dependencies{
		DrupalModules: []DrupalModuleDependency{
			{
				Module:          "islandora_fcrepo",
				ComposerPackage: "drupal/islandora_fcrepo",
				Mode:            DrupalModuleDependencyStrict,
			},
			{
				Module:          "islandora_triplestore",
				ComposerPackage: "drupal/islandora_triplestore",
				Mode:            DrupalModuleDependencyEnableOnly,
			},
			{
				Module: "",
				Mode:   DrupalModuleDependencyStrict,
			},
		},
	}

	modules := deps.DrupalModulesForEnable()
	if len(modules) != 2 {
		t.Fatalf("expected 2 enable modules, got %d", len(modules))
	}
	if modules[0].Module != "islandora_fcrepo" {
		t.Fatalf("expected first module islandora_fcrepo, got %q", modules[0].Module)
	}
	if modules[1].Module != "islandora_triplestore" {
		t.Fatalf("expected second module islandora_triplestore, got %q", modules[1].Module)
	}
}

func TestDependenciesStrictDrupalModules(t *testing.T) {
	t.Parallel()

	deps := Dependencies{
		DrupalModules: []DrupalModuleDependency{
			{
				Module:          "islandora_fcrepo",
				ComposerPackage: "drupal/islandora_fcrepo",
				Mode:            DrupalModuleDependencyStrict,
			},
			{
				Module:          "islandora_triplestore",
				ComposerPackage: "drupal/islandora_triplestore",
				Mode:            DrupalModuleDependencyEnableOnly,
			},
		},
	}

	modules := deps.StrictDrupalModules()
	if len(modules) != 1 {
		t.Fatalf("expected 1 strict module, got %d", len(modules))
	}
	if modules[0].Module != "islandora_fcrepo" {
		t.Fatalf("expected strict module islandora_fcrepo, got %q", modules[0].Module)
	}
}

func TestDefinitionDrupalModuleHelpers(t *testing.T) {
	t.Parallel()

	def := Definition{
		Dependencies: Dependencies{
			DrupalModules: []DrupalModuleDependency{
				{
					Module:          "islandora_fcrepo",
					ComposerPackage: "drupal/islandora_fcrepo",
					Mode:            DrupalModuleDependencyStrict,
				},
				{
					Module:          "islandora_triplestore",
					ComposerPackage: "drupal/islandora_triplestore",
					Mode:            DrupalModuleDependencyEnableOnly,
				},
			},
		},
	}

	enableModules := def.DrupalModulesForEnable()
	if len(enableModules) != 2 {
		t.Fatalf("expected 2 enable modules, got %d", len(enableModules))
	}

	strictModules := def.StrictDrupalModules()
	if len(strictModules) != 1 {
		t.Fatalf("expected 1 strict module, got %d", len(strictModules))
	}
	if strictModules[0].Module != "islandora_fcrepo" {
		t.Fatalf("expected strict module islandora_fcrepo, got %q", strictModules[0].Module)
	}

	enablePackages := def.ComposerPackagesForEnable()
	if len(enablePackages) != 2 {
		t.Fatalf("expected 2 composer packages, got %d", len(enablePackages))
	}
	if enablePackages[0] != "drupal/islandora_fcrepo" {
		t.Fatalf("expected first composer package drupal/islandora_fcrepo, got %q", enablePackages[0])
	}

	strictPackages := def.StrictComposerPackages()
	if len(strictPackages) != 1 {
		t.Fatalf("expected 1 strict composer package, got %d", len(strictPackages))
	}
	if strictPackages[0] != "drupal/islandora_fcrepo" {
		t.Fatalf("expected strict composer package drupal/islandora_fcrepo, got %q", strictPackages[0])
	}
}

func TestDefinitionCreateOptionIncludesPromptOnCreate(t *testing.T) {
	t.Parallel()

	def := Definition{
		Name:           "fcrepo",
		DefaultState:   StateOn,
		Guidance:       StateGuidance{Question: "fcrepo?"},
		PromptOnCreate: true,
	}

	option := def.CreateOption()
	if option.Name != "fcrepo" {
		t.Fatalf("expected option name fcrepo, got %q", option.Name)
	}
	if option.Default != StateOn {
		t.Fatalf("expected option default on, got %q", option.Default)
	}
	if !option.PromptOnCreate {
		t.Fatal("expected option to prompt on create")
	}
	if option.Guidance.Question != "fcrepo?" {
		t.Fatalf("expected guidance question preserved, got %q", option.Guidance.Question)
	}
}

func TestDependenciesComposerPackagesForEnableDeduplicatesSubmodules(t *testing.T) {
	t.Parallel()

	deps := Dependencies{
		DrupalModules: []DrupalModuleDependency{
			{
				Module:          "islandora_iiif",
				ComposerPackage: "drupal/islandora",
				Mode:            DrupalModuleDependencyEnableOnly,
			},
			{
				Module:          "islandora_fits",
				ComposerPackage: "drupal/islandora",
				Mode:            DrupalModuleDependencyEnableOnly,
			},
			{
				Module:          "islandora_fcrepo",
				ComposerPackage: "drupal/islandora_fcrepo",
				Mode:            DrupalModuleDependencyStrict,
			},
		},
	}

	packages := deps.ComposerPackagesForEnable()
	if len(packages) != 2 {
		t.Fatalf("expected 2 composer packages, got %d", len(packages))
	}
	if packages[0] != "drupal/islandora" {
		t.Fatalf("expected first package drupal/islandora, got %q", packages[0])
	}
	if packages[1] != "drupal/islandora_fcrepo" {
		t.Fatalf("expected second package drupal/islandora_fcrepo, got %q", packages[1])
	}
}
