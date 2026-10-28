package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/dexadata/dexaflow/internal/envcompat"
)

// Setting describes one configuration key as the loaders read it. It is the
// raw material of the generated configuration reference page
// (internal/tools/configref): every field is derived from the structs and the
// default maps the loaders use, so the page cannot name a key, a variable or a
// default the code does not have.
type Setting struct {
	// Key is the dotted key in the YAML config file, e.g. "auth.oidc.issuer".
	Key string
	// Env is the DEXAFLOW_* variable that sets the key, or "" when only the
	// config file can set it.
	Env string
	// LegacyEnv is the LEOFLOW_* variable from before the rename, read when Env
	// is unset; "" when Env is.
	LegacyEnv string
	// Type is a short type name: string, int, bool, float, duration, list or map.
	Type string
	// Default is the built-in value, rendered as text ("" when empty).
	Default string
	// Owner is the Go struct type that holds the field, e.g. "OIDCSection".
	Owner string
	// Field is the Go field name in Owner, e.g. "Issuer".
	Field string
}

// ServerSettings lists every key of ServerConfig in struct order, with the
// defaults LoadServer applies.
func ServerSettings() []Setting {
	return walkSettings(reflect.TypeOf(ServerConfig{}), serverDefaults, ".")
}

// CLISettings lists every key of the developer CLI Config in struct order,
// with the defaults Load applies.
func CLISettings() []Setting {
	return walkSettings(reflect.TypeOf(Config{}), cliDefaults(), "-")
}

// walkSettings flattens the struct t into settings. A key with an entry in
// defaults binds from the environment (the loaders register only those keys
// with viper before reading the environment); any other key is file-only.
// envSep is the separator the loader's key replacer turns into "_".
func walkSettings(t reflect.Type, defaults map[string]any, envSep string) []Setting {
	var out []Setting
	var walk func(t reflect.Type, prefix string)
	walk = func(t reflect.Type, prefix string) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if isSquashed(f) {
				walk(f.Type, prefix)
				continue
			}
			name := settingName(f)
			if name == "" {
				continue
			}
			key := prefix + name
			if f.Type.Kind() == reflect.Struct && f.Type != reflect.TypeOf(time.Duration(0)) {
				walk(f.Type, key+".")
				continue
			}
			s := Setting{Key: key, Type: settingType(f.Type), Owner: t.Name(), Field: f.Name}
			if def, ok := defaults[key]; ok {
				suffix := strings.ToUpper(strings.NewReplacer(".", "_", envSep, "_").Replace(key))
				s.Env = envcompat.NewPrefix + suffix
				s.LegacyEnv = envcompat.LegacyPrefix + suffix
				s.Default = renderDefault(def)
			}
			out = append(out, s)
		}
	}
	walk(t, "")
	return out
}

// isSquashed reports whether f is a struct whose fields mapstructure decodes
// at the level of the struct that holds it (`mapstructure:",squash"`), as
// DeployDefaultsSection does with domain.DeployDefaults.
func isSquashed(f reflect.StructField) bool {
	if f.Type.Kind() != reflect.Struct {
		return false
	}
	_, opts, _ := strings.Cut(f.Tag.Get("mapstructure"), ",")
	for _, opt := range strings.Split(opts, ",") {
		if opt == "squash" {
			return true
		}
	}
	return false
}

// settingName is the config key segment of a field: its mapstructure tag, or,
// for a field viper does not decode (mapstructure:"-"), its yaml tag, which is
// what the out-of-band decoder reads (decodeDottedOIDCMaps).
func settingName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("mapstructure"), ",")
	if name != "-" && name != "" {
		return name
	}
	yamlName, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
	if yamlName == "-" {
		return ""
	}
	return yamlName
}

// settingType names the kind of value a field holds.
func settingType(t reflect.Type) string {
	if t == reflect.TypeOf(time.Duration(0)) {
		return "duration"
	}
	switch t.Kind() {
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "int"
	case reflect.Float32, reflect.Float64:
		return "float"
	case reflect.Slice:
		return "list"
	case reflect.Map:
		return "map"
	case reflect.Pointer:
		// An optional value: unset means a default worked out at run time.
		return settingType(t.Elem())
	default:
		return "string"
	}
}

// renderDefault writes a default value the way an operator would type it:
// lists comma-separated (the form one environment variable takes), floats in
// their shortest form.
func renderDefault(v any) string {
	switch d := v.(type) {
	case nil:
		return ""
	case []string:
		return strings.Join(d, ",")
	case float64:
		return strconv.FormatFloat(d, 'g', -1, 64)
	case string:
		return d
	default:
		return fmt.Sprint(d)
	}
}
