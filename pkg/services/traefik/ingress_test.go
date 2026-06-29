package traefik

import (
	"testing"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/spf13/cobra"
)

func TestIngressCreateDefaultsDoNotPrompt(t *testing.T) {
	t.Parallel()

	ingress, err := Ingress(IngressOptions{NoAppService: true})
	if err != nil {
		t.Fatalf("Ingress() error = %v", err)
	}
	option := ingress.Definition().CreateOption()
	cmd := &cobra.Command{Use: "create"}
	corecomponent.AddCreateFlags(cmd, option)

	decisions, err := corecomponent.ResolveCreateDecisions(cmd, func(question ...string) (string, error) {
		t.Fatalf("did not expect create prompt for ingress defaults: %v", question)
		return "", nil
	}, option)
	if err != nil {
		t.Fatalf("ResolveCreateDecisions() error = %v", err)
	}

	decision := decisions[IngressName]
	if decision.Disposition != corecomponent.DispositionEnabled {
		t.Fatalf("ingress disposition = %q, want %q", decision.Disposition, corecomponent.DispositionEnabled)
	}
	if decision.State != corecomponent.StateOn {
		t.Fatalf("ingress state = %q, want %q", decision.State, corecomponent.StateOn)
	}

	wantOptions := map[string]string{
		ingressModeName:   IngressModeHTTP,
		ingressDomainName: DefaultIngressDomain,
		uploadSizeName:    DefaultMaxUploadSize,
		uploadTimeoutName: DefaultUploadTimeout,
	}
	for name, want := range wantOptions {
		if got := decision.Options[name]; got != want {
			t.Fatalf("ingress option %q = %q, want %q", name, got, want)
		}
	}
	if _, ok := decision.Options[ingressACMEEmailName]; ok {
		t.Fatalf("ingress option %q should not be set by default", ingressACMEEmailName)
	}
	if _, ok := decision.Options[ingressTrustedIPName]; ok {
		t.Fatalf("ingress option %q should not be set by default", ingressTrustedIPName)
	}
}
