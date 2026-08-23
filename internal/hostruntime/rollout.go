package hostruntime

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// RolloutServeOptions controls the managed rollout service process.
type RolloutServeOptions struct {
	Port, JWKSURI, Audience, CustomClaims, Binary string
}

// ServeRollout validates the root-owned configuration and replaces sitectl with the rollout service.
func ServeRollout(options RolloutServeOptions) error {
	if err := validateRolloutServe(options); err != nil {
		return err
	}
	if options.Binary == "" {
		options.Binary = "/usr/local/bin/cloud-compose-rollout"
	}
	return syscall.Exec(options.Binary, []string{options.Binary}, os.Environ())
}

func validateRolloutServe(options RolloutServeOptions) error {
	port, err := strconv.Atoi(options.Port)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != options.Port {
		return fmt.Errorf("ROLLOUT_PORT must be an integer from 1 through 65535")
	}
	parsed, err := url.Parse(options.JWKSURI)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || strings.ContainsAny(options.JWKSURI, " \t\r\n") {
		return fmt.Errorf("ROLLOUT_JWKS_URI must be an HTTPS URL without whitespace")
	}
	if options.Audience == "" || strings.ContainsAny(options.Audience, "\r\n") {
		return fmt.Errorf("ROLLOUT_JWT_AUD must be a non-empty single-line value")
	}
	if options.CustomClaims != "" {
		claims := map[string]any{}
		if err := json.Unmarshal([]byte(options.CustomClaims), &claims); err != nil || claims == nil {
			return fmt.Errorf("ROLLOUT_CUSTOM_CLAIMS must be empty or a JSON object")
		}
	}
	return nil
}
