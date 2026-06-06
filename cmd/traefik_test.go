package cmd

import (
	"strings"
	"testing"
)

func TestUpdateDotEnvTextUpdatesAndAppendsDeterministically(t *testing.T) {
	t.Parallel()

	got := updateDotEnvText("DOMAIN=example.org\nURI_SCHEME=\"http\"\n", map[string]string{}, map[string]string{
		"TLS_PROVIDER": "letsencrypt",
		"URI_SCHEME":   "https",
	})

	if !strings.Contains(got, "URI_SCHEME=\"https\"\n") {
		t.Fatalf("expected URI_SCHEME update, got:\n%s", got)
	}
	if !strings.Contains(got, "TLS_PROVIDER=\"letsencrypt\"\n") {
		t.Fatalf("expected TLS_PROVIDER append, got:\n%s", got)
	}
}

func TestSetTraefikLetsEncryptCommandTextUsesHTTPEntrypointForDrupalStyleCompose(t *testing.T) {
	t.Parallel()

	raw := `services:
  traefik:
    command:
      --entryPoints.http.address=:80
      --entryPoints.https.address=:443
    volumes:
      - acme-data:/acme:rw
`
	got, err := setTraefikLetsEncryptCommandText(raw, true)
	if err != nil {
		t.Fatalf("setTraefikLetsEncryptCommandText() error = %v", err)
	}

	for _, want := range []string{
		"--certificatesresolvers.letsencrypt.acme.storage=/acme/acme.json",
		"--certificatesresolvers.letsencrypt.acme.httpchallenge.entrypoint=http",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in updated compose, got:\n%s", want, got)
		}
	}
}

func TestSetTraefikLetsEncryptCommandTextUsesWebEntrypointForModernCompose(t *testing.T) {
	t.Parallel()

	raw := `services:
  traefik:
    command:
      - --entrypoints.web.address=:80
      - --entrypoints.websecure.address=:443
`
	got, err := setTraefikLetsEncryptCommandText(raw, true)
	if err != nil {
		t.Fatalf("setTraefikLetsEncryptCommandText() error = %v", err)
	}

	for _, want := range []string{
		"--certificatesresolvers.letsencrypt.acme.storage=/letsencrypt/acme.json",
		"--certificatesresolvers.letsencrypt.acme.httpchallenge.entrypoint=web",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in updated compose, got:\n%s", want, got)
		}
	}
}

func TestSetTraefikLetsEncryptCommandTextRemovesExistingACMELines(t *testing.T) {
	t.Parallel()

	raw := `services:
  traefik:
    command:
      - --entrypoints.web.address=:80
      - --certificatesresolvers.letsencrypt.acme.email=${ACME_EMAIL:?set ACME_EMAIL}
      - --certificatesresolvers.letsencrypt.acme.storage=/letsencrypt/acme.json
`
	got, err := setTraefikLetsEncryptCommandText(raw, false)
	if err != nil {
		t.Fatalf("setTraefikLetsEncryptCommandText() error = %v", err)
	}

	if strings.Contains(got, "certificatesresolvers.letsencrypt.acme") {
		t.Fatalf("expected ACME lines removed, got:\n%s", got)
	}
}

func TestSetTraefikLetsEncryptCommandTextHandlesFlowStyleCompose(t *testing.T) {
	t.Parallel()

	raw := `services: {traefik: {image: traefik:v3, command: ["--entrypoints.web.address=:80"]}}
`
	got, err := setTraefikLetsEncryptCommandText(raw, true)
	if err != nil {
		t.Fatalf("setTraefikLetsEncryptCommandText() error = %v", err)
	}

	for _, want := range []string{
		"--entrypoints.web.address=:80",
		"--certificatesresolvers.letsencrypt.acme.httpchallenge.entrypoint=web",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in updated compose, got:\n%s", want, got)
		}
	}
}

func TestSetTraefikLetsEncryptCommandTextNoopsWithoutTraefikService(t *testing.T) {
	t.Parallel()

	raw := "services:\n  app:\n    image: example\n"
	got, err := setTraefikLetsEncryptCommandText(raw, true)
	if err != nil {
		t.Fatalf("setTraefikLetsEncryptCommandText() error = %v", err)
	}
	if got != raw {
		t.Fatalf("expected compose without traefik service to remain unchanged, got:\n%s", got)
	}
}
