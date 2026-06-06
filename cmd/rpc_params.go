package cmd

import (
	"fmt"
	"strings"

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
