package flags

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/spf13/pflag"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// RegisterProtoFlags automatically registers flags for all fields in a protobuf message
// It uses protobuf field names and converts them to kebab-case
func RegisterProtoFlags(flagSet *pflag.FlagSet, msg proto.Message) error {
	msgReflect := msg.ProtoReflect()
	descriptor := msgReflect.Descriptor()
	fields := descriptor.Fields()

	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		fieldName := string(field.Name())
		flagName := toKebabCase(fieldName)

		// Skip fields that are typically auto-generated or read-only
		if strings.HasSuffix(fieldName, "_id") || fieldName == "status" {
			continue
		}

		switch field.Kind() {
		case protoreflect.BoolKind:
			flagSet.Bool(flagName, false, fmt.Sprintf("%s (optional)", fieldName))
		case protoreflect.Int32Kind, protoreflect.Int64Kind:
			flagSet.Int32(flagName, 0, fmt.Sprintf("%s (optional)", fieldName))
		case protoreflect.Uint32Kind, protoreflect.Uint64Kind:
			flagSet.Uint32(flagName, 0, fmt.Sprintf("%s (optional)", fieldName))
		case protoreflect.StringKind:
			flagSet.String(flagName, "", fmt.Sprintf("%s (optional)", fieldName))
		case protoreflect.EnumKind:
			flagSet.String(flagName, "", fmt.Sprintf("%s (optional, enum)", fieldName))
		case protoreflect.MessageKind:
			// Skip nested messages for now
			continue
		}
	}

	return nil
}

// LoadProtoFromFlags loads flag values into a protobuf message
func LoadProtoFromFlags(flagSet *pflag.FlagSet, msg proto.Message) error {
	msgReflect := msg.ProtoReflect()
	descriptor := msgReflect.Descriptor()
	fields := descriptor.Fields()

	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		fieldName := string(field.Name())
		flagName := toKebabCase(fieldName)

		// Skip if flag doesn't exist or wasn't changed
		if !flagSet.Changed(flagName) {
			continue
		}

		switch field.Kind() {
		case protoreflect.BoolKind:
			v, err := flagSet.GetBool(flagName)
			if err != nil {
				return fmt.Errorf("error getting flag %q: %w", flagName, err)
			}
			msgReflect.Set(field, protoreflect.ValueOfBool(v))

		case protoreflect.Int32Kind:
			v, err := flagSet.GetInt32(flagName)
			if err != nil {
				return fmt.Errorf("error getting flag %q: %w", flagName, err)
			}
			msgReflect.Set(field, protoreflect.ValueOfInt32(v))

		case protoreflect.Int64Kind:
			v, err := flagSet.GetInt64(flagName)
			if err != nil {
				return fmt.Errorf("error getting flag %q: %w", flagName, err)
			}
			msgReflect.Set(field, protoreflect.ValueOfInt64(v))

		case protoreflect.Uint32Kind:
			v, err := flagSet.GetUint32(flagName)
			if err != nil {
				return fmt.Errorf("error getting flag %q: %w", flagName, err)
			}
			msgReflect.Set(field, protoreflect.ValueOfUint32(v))

		case protoreflect.Uint64Kind:
			v, err := flagSet.GetUint64(flagName)
			if err != nil {
				return fmt.Errorf("error getting flag %q: %w", flagName, err)
			}
			msgReflect.Set(field, protoreflect.ValueOfUint64(v))

		case protoreflect.StringKind:
			v, err := flagSet.GetString(flagName)
			if err != nil {
				return fmt.Errorf("error getting flag %q: %w", flagName, err)
			}
			msgReflect.Set(field, protoreflect.ValueOfString(v))

		case protoreflect.EnumKind:
			v, err := flagSet.GetString(flagName)
			if err != nil {
				return fmt.Errorf("error getting flag %q: %w", flagName, err)
			}
			// Convert string to enum value
			enumDesc := field.Enum()
			enumValue := enumDesc.Values().ByName(protoreflect.Name(v))
			if enumValue == nil {
				// Try with uppercase prefix
				enumValue = enumDesc.Values().ByName(protoreflect.Name(strings.ToUpper(v)))
			}
			if enumValue != nil {
				msgReflect.Set(field, protoreflect.ValueOfEnum(enumValue.Number()))
			}
		}
	}

	return nil
}

// toKebabCase converts snake_case or camelCase to kebab-case
func toKebabCase(s string) string {
	return strings.ReplaceAll(s, "_", "-")
}

// LoadFromFlags loads flag values into a struct using reflection (similar to your config example)
func LoadFromFlags(flagSet *pflag.FlagSet, target interface{}, existingData interface{}) error {
	t := reflect.TypeOf(target).Elem()
	v := reflect.ValueOf(target).Elem()

	// Check if we're updating existing data
	exists := existingData != nil && !reflect.ValueOf(existingData).IsZero()

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		// Try multiple tag sources: protobuf json tag, yaml tag, or field name
		tag := field.Tag.Get("json")
		if tag == "" || tag == "-" {
			tag = field.Tag.Get("yaml")
		}
		if tag == "" || tag == "-" {
			tag = field.Name
		}

		// Clean up tag
		tag = strings.Split(tag, ",")[0]
		flagName := toKebabCase(tag)

		// Skip if field shouldn't be a flag
		if tag == "-" || tag == "" {
			continue
		}

		// Skip map types as they're not supported as flags
		if field.Type.Kind() == reflect.Map {
			continue
		}

		// If updating existing data, skip unchanged flags
		if exists && !flagSet.Changed(flagName) {
			continue
		}

		// Check if flag exists before trying to get it
		flag := flagSet.Lookup(flagName)
		if flag == nil {
			continue
		}

		var value interface{}
		switch field.Type.Kind() {
		case reflect.Bool:
			val, err := flagSet.GetBool(flagName)
			if err != nil {
				return fmt.Errorf("error getting flag %q: %w", flagName, err)
			}
			value = val

		case reflect.Int, reflect.Int32, reflect.Int64:
			val, err := flagSet.GetInt64(flagName)
			if err != nil {
				return fmt.Errorf("error getting flag %q: %w", flagName, err)
			}
			value = val

		case reflect.Uint, reflect.Uint32, reflect.Uint64:
			val, err := flagSet.GetUint64(flagName)
			if err != nil {
				return fmt.Errorf("error getting flag %q: %w", flagName, err)
			}
			value = val

		case reflect.String:
			val, err := flagSet.GetString(flagName)
			if err != nil {
				return fmt.Errorf("error getting flag %q: %w", flagName, err)
			}
			value = val

		case reflect.Slice:
			if field.Type.Elem().Kind() == reflect.String {
				val, err := flagSet.GetStringSlice(flagName)
				if err != nil {
					return fmt.Errorf("error getting string slice flag %q: %w", flagName, err)
				}
				value = val
			}
		}

		// Set the value
		if value != nil {
			fieldValue := v.Field(i)
			if fieldValue.CanSet() {
				val := reflect.ValueOf(value)
				if val.Type().ConvertibleTo(fieldValue.Type()) {
					fieldValue.Set(val.Convert(fieldValue.Type()))
				}
			}
		}
	}

	return nil
}
