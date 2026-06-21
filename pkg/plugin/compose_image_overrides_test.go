package plugin

import "testing"

func TestResolveComposeImageOverridesBuildkitTagUsesApplicationBaseImages(t *testing.T) {
	cases := []struct {
		plugin  string
		service string
		image   string
	}{
		{plugin: "drupal", service: "drupal", image: "libops/drupal:nginx-1.30.2-php84"},
		{plugin: "isle", service: "drupal", image: "libops/islandora:nginx-1.30.2-php84"},
		{plugin: "islandora", service: "drupal", image: "libops/islandora:nginx-1.30.2-php84"},
		{plugin: "ojs", service: "ojs", image: "libops/ojs:nginx-1.30.2-php84"},
		{plugin: "omeka-classic", service: "omeka-classic", image: "libops/omeka-classic:nginx-1.30.2-php84"},
		{plugin: "omeka-s", service: "omeka-s", image: "libops/omeka-s:nginx-1.30.2-php84"},
		{plugin: "wp", service: "wp", image: "libops/wp:nginx-1.30.2-php84"},
	}

	for _, tt := range cases {
		t.Run(tt.plugin, func(t *testing.T) {
			overrides, err := ResolveComposeImageOverrides(tt.plugin, "libops", "nginx-1.30.2-php84", nil, nil)
			if err != nil {
				t.Fatalf("ResolveComposeImageOverrides() error = %v", err)
			}
			args := overrides.BuildArgs[tt.service]
			if args["BASE_IMAGE"] != tt.image {
				t.Fatalf("BASE_IMAGE for %s = %q, want %q", tt.service, args["BASE_IMAGE"], tt.image)
			}
		})
	}
}
