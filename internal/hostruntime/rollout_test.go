package hostruntime

import "testing"

func TestValidateRolloutServe(t *testing.T) {
	valid := RolloutServeOptions{Port: "8080", JWKSURI: "https://issuer.example/jwks", Audience: "audience", CustomClaims: `{"role":"admin"}`}
	if err := validateRolloutServe(valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RolloutServeOptions){
		"port":   func(options *RolloutServeOptions) { options.Port = "080" },
		"jwks":   func(options *RolloutServeOptions) { options.JWKSURI = "http://issuer.example" },
		"aud":    func(options *RolloutServeOptions) { options.Audience = "" },
		"claims": func(options *RolloutServeOptions) { options.CustomClaims = "[]" },
	} {
		t.Run(name, func(t *testing.T) {
			options := valid
			mutate(&options)
			if err := validateRolloutServe(options); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}
