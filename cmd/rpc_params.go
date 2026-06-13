package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/libops/sitectl/pkg/plugin"
)

func extractConvergeRPCParams(args []string) (plugin.ConvergeRunParams, []string, error) {
	return plugin.ExtractRPCParamsFromArgs[plugin.ConvergeRunParams](args)
}

func extractSetRPCParams(args []string) (plugin.SetRunParams, []string, error) {
	return plugin.ExtractRPCParamsFromArgs[plugin.SetRunParams](args)
}

func extractValidateRPCParams(args []string) (string, plugin.ValidateRunParams, []string, error) {
	// --format is host-owned: core writes the merged validation report after the
	// plugin returns typed results, so it must not be bridged back into plugin
	// argv with ValidateRunParams.
	format, remaining, err := extractValidateFormat(args)
	if err != nil {
		return "", plugin.ValidateRunParams{}, nil, err
	}
	params, passthrough, err := plugin.ExtractRPCParamsFromArgs[plugin.ValidateRunParams](remaining)
	return format, params, passthrough, err
}

func extractVerifyRPCParams(args []string) (string, plugin.VerifyRunParams, []string, error) {
	format, remaining, err := extractValidateFormat(args)
	if err != nil {
		return "", plugin.VerifyRunParams{}, nil, err
	}
	params, passthrough, err := plugin.ExtractRPCParamsFromArgs[plugin.VerifyRunParams](remaining)
	return format, params, passthrough, err
}

type healthcheckHostParams struct {
	Format   string
	Persist  bool
	Timeout  time.Duration
	Interval time.Duration
}

func extractHealthcheckRPCParams(args []string) (healthcheckHostParams, plugin.HealthcheckRunParams, []string, error) {
	hostParams, remaining, err := extractHealthcheckHostParams(args)
	if err != nil {
		return healthcheckHostParams{}, plugin.HealthcheckRunParams{}, nil, err
	}
	params, passthrough, err := plugin.ExtractRPCParamsFromArgs[plugin.HealthcheckRunParams](remaining)
	return hostParams, params, passthrough, err
}

func extractComponentSetRPCParams(args []string) (plugin.ComponentSetParams, []string, error) {
	params, passthrough, err := plugin.ExtractRPCParamsFromArgs[plugin.ComponentSetParams](args)
	if err != nil {
		return plugin.ComponentSetParams{}, nil, err
	}
	if strings.TrimSpace(params.Name) == "" {
		return plugin.ComponentSetParams{}, nil, fmt.Errorf("component name is required")
	}
	return params, passthrough, nil
}

func extractHealthcheckHostParams(args []string) (healthcheckHostParams, []string, error) {
	passthrough := make([]string, 0, len(args))
	params := healthcheckHostParams{
		Timeout:  5 * time.Minute,
		Interval: 10 * time.Second,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			passthrough = append(passthrough, args[i:]...)
			break
		}
		if !strings.HasPrefix(arg, "--") {
			passthrough = append(passthrough, arg)
			continue
		}

		raw := strings.TrimPrefix(arg, "--")
		name, value, hasValue := strings.Cut(raw, "=")
		switch name {
		case "format", "timeout", "interval":
			if !hasValue {
				if i+1 >= len(args) {
					return healthcheckHostParams{}, nil, fmt.Errorf("--%s requires a value", name)
				}
				i++
				value = args[i]
			}
		case "persist":
			if !hasValue {
				value = "true"
			}
		default:
			passthrough = append(passthrough, arg)
			continue
		}

		switch name {
		case "format":
			params.Format = value
		case "persist":
			persist, err := strconv.ParseBool(value)
			if err != nil {
				return healthcheckHostParams{}, nil, fmt.Errorf("parse --persist: %w", err)
			}
			params.Persist = persist
		case "timeout":
			timeout, err := time.ParseDuration(value)
			if err != nil {
				return healthcheckHostParams{}, nil, fmt.Errorf("parse --timeout: %w", err)
			}
			params.Timeout = timeout
		case "interval":
			interval, err := time.ParseDuration(value)
			if err != nil {
				return healthcheckHostParams{}, nil, fmt.Errorf("parse --interval: %w", err)
			}
			params.Interval = interval
		}
	}
	return params, passthrough, nil
}

func extractValidateFormat(args []string) (string, []string, error) {
	passthrough := make([]string, 0, len(args))
	var format string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			passthrough = append(passthrough, args[i:]...)
			break
		}
		if !strings.HasPrefix(arg, "--") {
			passthrough = append(passthrough, arg)
			continue
		}

		raw := strings.TrimPrefix(arg, "--")
		name, value, hasValue := strings.Cut(raw, "=")
		if name != "format" {
			passthrough = append(passthrough, arg)
			continue
		}
		if !hasValue {
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--format requires a value")
			}
			i++
			value = args[i]
		}
		format = value
	}
	return format, passthrough, nil
}
