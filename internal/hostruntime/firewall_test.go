package hostruntime

import "testing"

func TestMetadataFirewallPreDockerOmitsDockerChains(t *testing.T) {
	rules := metadataFirewallRules("pre-docker")
	if len(rules) != 5 {
		t.Fatalf("rules = %d", len(rules))
	}
	for _, rule := range rules {
		if rule.chain == "DOCKER-USER" || rule.chain == "FORWARD" {
			t.Fatalf("pre-Docker rules contain %s", rule.chain)
		}
	}
}

func TestMetadataFirewallFullPreservesDNSBeforeDrop(t *testing.T) {
	rules := metadataFirewallRules("full")
	var dockerRules []firewallRule
	for _, rule := range rules {
		if rule.chain == "DOCKER-USER" {
			dockerRules = append(dockerRules, rule)
		}
	}
	if len(dockerRules) != 3 {
		t.Fatalf("Docker rules = %d", len(dockerRules))
	}
	// Rules are inserted at position one. Declaring DROP before the DNS allows
	// leaves both DNS rules ahead of the final catch-all deny.
	if dockerRules[0].args[len(dockerRules[0].args)-1] != "DROP" ||
		dockerRules[1].args[len(dockerRules[1].args)-1] != "ACCEPT" ||
		dockerRules[2].args[len(dockerRules[2].args)-1] != "ACCEPT" {
		t.Fatalf("unexpected Docker rule order: %#v", dockerRules)
	}
}
