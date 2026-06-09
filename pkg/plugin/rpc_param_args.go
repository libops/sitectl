package plugin

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	rpcFlagsTag     = "rpc_flags"
	rpcPositionTag  = "rpc_pos"
	rpcRootfsTag    = "rpc_rootfs"
	rpcSensitiveTag = "rpc_sensitive"
	rpcMethodsTag   = "rpc_methods"
)

// RPC argv bridge invariants:
// ExtractRPCParamsFromArgs promotes only host-owned flags and unambiguous
// positionals into typed JSON params. appendRPCParamFlags and
// appendRPCParamPositionals reconstruct those params into cobra argv on the
// plugin side. Keep host extraction and plugin reconstruction changes covered
// by the round-trip tests in cmd/rpc_params_test.go and rpc_command_test.go.
// Empty string params use Go zero-value omission semantics; whitespace-only
// string values and dash-prefixed positionals are rejected before argv is built.

// RPCSensitiveParams marks a params value as unsafe for argv transport.
//
// InvokePluginRPC refuses to run interactive calls with Stdin set when the
// request was built from params that return true here. Implementations may use
// value or pointer receivers.
// Only typed Params are screened; never place secrets in Args, Context, or
// LogLevel.
// Core RPC params are currently secret-free; this interface and the
// rpc_sensitive struct tag are kept for future methods that may need secrets.
type RPCSensitiveParams interface {
	HasSensitiveRPCParams() bool
}

// MarkCodebaseRootfsFlag marks flagName as the command flag that should receive
// CodebaseRootfs RPC params when reconstructing argv for a plugin command.
func MarkCodebaseRootfsFlag(cmd *cobra.Command, flagName string) {
	corecomponent.MarkCodebaseRootfsFlag(cmd, flagName)
}

type rpcParamSpec struct {
	fieldIndex int
	fieldName  string
	flags      []string
	position   int
	hasPos     bool
	rootfs     bool
	kind       reflect.Kind
	methods    []string
}

// ExtractRPCParamsFromArgs promotes tagged RPC flags and positionals from args
// into a typed params struct, returning all unclaimed args as passthrough.
// Tagged fields currently support bool and string only; rpc_pos fields must be
// strings.
func ExtractRPCParamsFromArgs[T any](args []string) (T, []string, error) {
	var params T
	value := reflect.ValueOf(&params).Elem()
	specs, err := rpcParamSpecs(value.Type())
	if err != nil {
		return params, nil, err
	}

	byFlag := map[string]rpcParamSpec{}
	for _, spec := range specs {
		for _, flag := range spec.flags {
			if _, exists := byFlag[flag]; exists {
				return params, nil, fmt.Errorf("%s flag %q is declared more than once", value.Type().Name(), flag)
			}
			byFlag[flag] = spec
		}
	}

	passthrough := make([]string, 0, len(args))
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
		name, flagValue, hasValue := strings.Cut(raw, "=")
		spec, ok := byFlag[name]
		if !ok {
			passthrough = append(passthrough, arg)
			continue
		}

		if spec.kind == reflect.Bool {
			if !hasValue {
				flagValue = "true"
			}
			if err := setRPCParamField(value, spec, flagValue); err != nil {
				return params, nil, err
			}
			continue
		}

		if !hasValue {
			if i+1 >= len(args) {
				return params, nil, fmt.Errorf("--%s requires a value", name)
			}
			i++
			flagValue = args[i]
		}
		if err := setRPCParamField(value, spec, flagValue); err != nil {
			return params, nil, err
		}
	}

	passthrough, err = extractRPCPositionals(value, passthrough, specs)
	if err != nil {
		return params, nil, err
	}
	return params, passthrough, nil
}

func appendRPCParamPositionals(args []string, params any) ([]string, error) {
	value, specs, err := rpcParamValueAndSpecs(params)
	if err != nil {
		return nil, err
	}
	positionals := rpcPositionalSpecs(specs)
	for _, spec := range positionals {
		field := value.Field(spec.fieldIndex)
		if field.Kind() != reflect.String || field.String() == "" {
			continue
		}
		if err := validateRPCParamStringValue(value.Type().Name(), spec, field.String(), true); err != nil {
			return nil, err
		}
		args = append(args, field.String())
	}
	return args, nil
}

func appendRPCParamFlags(command *cobra.Command, subcommand string, args []string, params any) ([]string, error) {
	return appendRPCParamFlagsForMethod("", command, subcommand, args, params)
}

func appendRPCParamFlagsForMethod(method string, command *cobra.Command, subcommand string, args []string, params any) ([]string, error) {
	value, specs, err := rpcParamValueAndSpecs(params)
	if err != nil {
		return nil, err
	}
	if err := validateRPCParamSpecsForMethod(method, value, specs); err != nil {
		return nil, err
	}
	for _, spec := range specs {
		field := value.Field(spec.fieldIndex)
		if !rpcParamSpecAppliesToMethod(spec, method) {
			continue
		}
		if len(spec.flags) == 0 {
			continue
		}
		flag := rpcParamFlag(command, subcommand, spec)
		switch spec.kind {
		case reflect.Bool:
			if field.Bool() {
				args = append(args, flag)
			}
		case reflect.String:
			if field.String() != "" {
				if err := validateRPCParamStringValue(value.Type().Name(), spec, field.String(), false); err != nil {
					return nil, err
				}
				args = append(args, flag, field.String())
			}
		}
	}
	return args, nil
}

func rpcParamFlag(command *cobra.Command, subcommand string, spec rpcParamSpec) string {
	flag := spec.flags[0]
	if spec.rootfs {
		if declared := rpcCodebaseRootfsFlagName(rpcCommandTarget(command, subcommand)); declared != "" {
			flag = declared
		}
	}
	return "--" + flag
}

func rpcCodebaseRootfsFlagName(command *cobra.Command) string {
	if command == nil {
		return ""
	}
	var name string
	command.Flags().VisitAll(func(flag *pflag.Flag) {
		if name != "" {
			return
		}
		if len(flag.Annotations[corecomponent.CodebaseRootfsFlagAnnotation]) > 0 {
			name = flag.Name
		}
	})
	return name
}

func validateRPCParamsForMethod(method string, params any) error {
	value, specs, err := rpcParamValueAndSpecs(params)
	if err != nil {
		return err
	}
	return validateRPCParamSpecsForMethod(method, value, specs)
}

func validateRPCParamSpecsForMethod(method string, value reflect.Value, specs []rpcParamSpec) error {
	for _, spec := range specs {
		if rpcParamSpecAppliesToMethod(spec, method) {
			continue
		}
		if rpcParamFieldIsSet(value.Field(spec.fieldIndex)) {
			return fmt.Errorf("%s does not support %s.%s", method, value.Type().Name(), spec.fieldName)
		}
	}
	return nil
}

func validateRPCParamFlags(method string, command *cobra.Command, subcommand string, params any) error {
	value, specs, err := rpcParamValueAndSpecs(params)
	if err != nil {
		return err
	}
	target := rpcCommandTarget(command, subcommand)
	for _, spec := range specs {
		if !rpcParamSpecAppliesToMethod(spec, method) {
			continue
		}
		if len(spec.flags) == 0 {
			continue
		}
		flagName := strings.TrimPrefix(rpcParamFlag(command, subcommand, spec), "--")
		if target == nil {
			return fmt.Errorf("%s RPC command must declare --%s for %s.%s", method, flagName, value.Type().Name(), spec.fieldName)
		}
		flag := target.Flags().Lookup(flagName)
		if flag == nil {
			return fmt.Errorf("%s RPC command must declare --%s for %s.%s", method, flagName, value.Type().Name(), spec.fieldName)
		}
		if err := validateRPCParamFlagType(method, flag, value.Type().Name(), spec); err != nil {
			return err
		}
	}
	return nil
}

func validateRPCParamFlagType(method string, flag *pflag.Flag, typeName string, spec rpcParamSpec) error {
	want, ok := rpcParamFlagValueType(spec.kind)
	if !ok {
		return fmt.Errorf("%s.%s has unsupported RPC param kind %s", typeName, spec.fieldName, spec.kind)
	}
	got := ""
	if flag.Value != nil {
		got = flag.Value.Type()
	}
	if got != want {
		return fmt.Errorf("%s RPC command --%s must be a %s flag for %s.%s, got %s", method, flag.Name, want, typeName, spec.fieldName, got)
	}
	return nil
}

func rpcParamFlagValueType(kind reflect.Kind) (string, bool) {
	switch kind {
	case reflect.Bool:
		return "bool", true
	case reflect.String:
		return "string", true
	default:
		return "", false
	}
}

func mustValidateRPCParamFlags(method string, command *cobra.Command, subcommand string, params any) {
	if err := validateRPCParamFlags(method, command, subcommand, params); err != nil {
		panic(fmt.Sprintf("register %s RPC command: %v", method, err))
	}
}

func mustValidateRPCParamFlagsIfCommandExists(method string, command *cobra.Command, subcommand string, params any) {
	if !rpcCommandHasSubcommand(command, subcommand) {
		return
	}
	mustValidateRPCParamFlags(method, command, subcommand, params)
}

func validateRPCParamStringValue(typeName string, spec rpcParamSpec, value string, positional bool) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s.%s RPC argv value must not be only whitespace", typeName, spec.fieldName)
	}
	if positional && strings.HasPrefix(value, "-") {
		return fmt.Errorf("%s.%s RPC positional value must not start with -", typeName, spec.fieldName)
	}
	return nil
}

func rpcParamSpecAppliesToMethod(spec rpcParamSpec, method string) bool {
	if strings.TrimSpace(method) == "" || len(spec.methods) == 0 {
		return true
	}
	for _, allowed := range spec.methods {
		if allowed == method {
			return true
		}
	}
	return false
}

func rpcParamFieldIsSet(field reflect.Value) bool {
	switch field.Kind() {
	case reflect.Bool:
		return field.Bool()
	case reflect.String:
		return field.String() != ""
	default:
		return !field.IsZero()
	}
}

func extractRPCPositionals(value reflect.Value, passthrough []string, specs []rpcParamSpec) ([]string, error) {
	positionals := rpcPositionalSpecs(specs)
	for _, spec := range positionals {
		if spec.kind != reflect.String {
			return nil, fmt.Errorf("%s.%s positional field must be a string", value.Type().Name(), spec.fieldName)
		}
		index, arg, ok := firstRPCPositionalArg(passthrough)
		if !ok {
			continue
		}
		value.Field(spec.fieldIndex).SetString(arg)
		passthrough = append(append([]string{}, passthrough[:index]...), passthrough[index+1:]...)
	}
	return passthrough, nil
}

func firstRPCPositionalArg(args []string) (int, string, bool) {
	for i, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		if arg == "--" {
			return -1, "", false
		}
		if arg == "-h" || arg == "--help" {
			continue
		}
		if strings.HasPrefix(arg, "--") {
			if strings.Contains(strings.TrimPrefix(arg, "--"), "=") {
				continue
			}
			// Unknown long flags in space form may consume the following token
			// as their value, so stop before mistaking that value for a
			// component positional.
			return -1, "", false
		}
		if strings.HasPrefix(arg, "-") {
			return -1, "", false
		}
		return i, arg, true
	}
	return -1, "", false
}

func rpcPositionalSpecs(specs []rpcParamSpec) []rpcParamSpec {
	var positionals []rpcParamSpec
	for _, spec := range specs {
		if spec.hasPos {
			positionals = append(positionals, spec)
		}
	}
	sort.Slice(positionals, func(i, j int) bool {
		return positionals[i].position < positionals[j].position
	})
	return positionals
}

func setRPCParamField(value reflect.Value, spec rpcParamSpec, raw string) error {
	field := value.Field(spec.fieldIndex)
	switch spec.kind {
	case reflect.Bool:
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("--%s expects a boolean value: %w", spec.flags[0], err)
		}
		field.SetBool(parsed)
	case reflect.String:
		field.SetString(raw)
	default:
		return fmt.Errorf("%s.%s has unsupported RPC param kind %s", value.Type().Name(), spec.fieldName, spec.kind)
	}
	return nil
}

func rpcParamValueAndSpecs(params any) (reflect.Value, []rpcParamSpec, error) {
	value := reflect.ValueOf(params)
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return reflect.Value{}, nil, fmt.Errorf("nil RPC params")
		}
		value = value.Elem()
	}
	specs, err := rpcParamSpecs(value.Type())
	return value, specs, err
}

func rpcParamSpecs(typ reflect.Type) ([]rpcParamSpec, error) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil, fmt.Errorf("RPC params must be a struct, got %s", typ.Kind())
	}

	var specs []rpcParamSpec
	seenFlags := map[string]string{}
	seenPositions := map[int]string{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		spec := rpcParamSpec{
			fieldIndex: i,
			fieldName:  field.Name,
			flags:      parseRPCParamList(field.Tag.Get(rpcFlagsTag)),
			rootfs:     field.Tag.Get(rpcRootfsTag) == "true",
			kind:       field.Type.Kind(),
			position:   -1,
			methods:    parseRPCParamList(field.Tag.Get(rpcMethodsTag)),
		}
		if rawPosition := strings.TrimSpace(field.Tag.Get(rpcPositionTag)); rawPosition != "" {
			position, err := strconv.Atoi(rawPosition)
			if err != nil || position < 0 {
				return nil, fmt.Errorf("%s.%s has invalid %s tag %q", typ.Name(), field.Name, rpcPositionTag, rawPosition)
			}
			spec.position = position
			spec.hasPos = true
		}
		if len(spec.flags) == 0 && !spec.hasPos {
			continue
		}
		switch field.Type.Kind() {
		case reflect.Bool, reflect.String:
		default:
			return nil, fmt.Errorf("%s.%s has unsupported RPC param kind %s", typ.Name(), field.Name, field.Type.Kind())
		}
		for _, flag := range spec.flags {
			if previous := seenFlags[flag]; previous != "" {
				return nil, fmt.Errorf("%s flag %q is declared more than once by %s and %s", typ.Name(), flag, previous, field.Name)
			}
			seenFlags[flag] = field.Name
		}
		if spec.hasPos {
			if previous := seenPositions[spec.position]; previous != "" {
				return nil, fmt.Errorf("%s position %d is declared more than once by %s and %s", typ.Name(), spec.position, previous, field.Name)
			}
			seenPositions[spec.position] = field.Name
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func parseRPCParamList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimPrefix(strings.TrimSpace(part), "--")
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func rpcParamsAreSensitive(params any) bool {
	if params == nil {
		return false
	}
	if hasSensitiveRPCParams(params) {
		return true
	}
	value := reflect.ValueOf(params)
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return false
		}
		if value.CanInterface() && hasSensitiveRPCParams(value.Interface()) {
			return true
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return false
	}
	if hasSensitiveRPCParamsPointer(value) {
		return true
	}
	typ := value.Type()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.IsExported() && field.Tag.Get(rpcSensitiveTag) == "true" {
			return true
		}
	}
	return false
}

func hasSensitiveRPCParams(params any) bool {
	sensitive, ok := params.(RPCSensitiveParams)
	return ok && sensitive.HasSensitiveRPCParams()
}

func hasSensitiveRPCParamsPointer(value reflect.Value) bool {
	if value.Kind() != reflect.Struct || !value.CanInterface() {
		return false
	}
	pointer := reflect.New(value.Type())
	pointer.Elem().Set(value)
	return hasSensitiveRPCParams(pointer.Interface())
}
