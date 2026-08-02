package cmd

import (
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
)

func TestGenerateSecretValueFormats(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, prefix string
		length       int
	}{
		{name: "hex32", length: 64}, {name: "base64-32", length: 43}, {name: "laravel-base64", prefix: "base64:", length: 51}, {name: "salt74", length: 74},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := generateSecretValue(test.name)
			if err != nil {
				t.Fatal(err)
			}
			if len(value) != test.length || !strings.HasPrefix(value, test.prefix) {
				t.Fatalf("generateSecretValue(%q) = %q", test.name, value)
			}
		})
	}
	if _, err := generateSecretValue("unknown"); err == nil {
		t.Fatal("unsupported format was accepted")
	}
}

func TestSafeProjectRelativePath(t *testing.T) {
	t.Parallel()
	ctx := &config.Context{ProjectDir: "/srv/site"}
	got, err := safeProjectRelativePath(ctx, "backups/one")
	if err != nil || got != "/srv/site/backups/one" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, value := range []string{"/tmp/backup", "../backup", "."} {
		if _, err := safeProjectRelativePath(ctx, value); err == nil {
			t.Errorf("unsafe path %q was accepted", value)
		}
	}
}

func TestDoctorHintsAreActionable(t *testing.T) {
	t.Parallel()
	for _, detail := range []string{"health=starting", "unhealthy", "service missing", "container exited", "unknown"} {
		if hint := doctorHint(detail); !strings.Contains(hint, "sitectl") {
			t.Errorf("hint for %q is not actionable: %q", detail, hint)
		}
	}
}

func TestSecretRotationConsequences(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"WORDPRESS_AUTH_KEY", "DRUPAL_DEFAULT_SALT", "OJS_SECRET_KEY", "DB_ROOT_PASSWORD"} {
		if secretRotationConsequence(name) == "" {
			t.Errorf("missing consequence for %s", name)
		}
	}
}
