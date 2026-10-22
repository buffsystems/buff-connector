package plugin

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// A deliberately small subset of JSON Schema, implemented here rather than
// pulled in as a dependency.
//
// The connector ships with two direct dependencies, and that number is part of
// how we argue it is cheap to audit — a full schema library would cost more than
// it buys for the handful of keywords a plugin's settings form needs. The subset
// is enforced at BOTH ends: the publisher's validator refuses a manifest that
// reaches outside it, and so does this parser. A keyword we would silently
// ignore is worse than one we reject, because everyone downstream assumes the
// constraint holds.

// Schema is one node of the supported subset.
type Schema struct {
	Type      string   `yaml:"type" json:"type"`
	Format    string   `yaml:"format,omitempty" json:"format,omitempty"`
	Title     string   `yaml:"title,omitempty" json:"title,omitempty"`
	Desc      string   `yaml:"description,omitempty" json:"description,omitempty"`
	Enum      []string `yaml:"enum,omitempty" json:"enum,omitempty"`
	Pattern   string   `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	MinLength *int     `yaml:"minLength,omitempty" json:"minLength,omitempty"`
	Minimum   *float64 `yaml:"minimum,omitempty" json:"minimum,omitempty"`
	Maximum   *float64 `yaml:"maximum,omitempty" json:"maximum,omitempty"`
	Secret    bool     `yaml:"secret,omitempty" json:"secret,omitempty"`
	// Default is what a setting is when the operator leaves it empty: the form
	// shows it, and the connector fills it in before the plugin, the egress rules
	// or the validator see the config (Manifest.WithDefaults). Scalars only — a
	// default list or object is a shape nobody can read off a form.
	Default    any                `yaml:"default,omitempty" json:"default,omitempty"`
	Required   []string           `yaml:"required,omitempty" json:"required,omitempty"`
	Properties map[string]*Schema `yaml:"properties,omitempty" json:"properties,omitempty"`
	// Order is the property names as the manifest wrote them. A map forgets
	// that, and a form drawn from a map puts «Исключения» above «Папки» for no
	// reason a person can see.
	Order []string `yaml:"-" json:"order,omitempty"`
	Items *Schema  `yaml:"items,omitempty" json:"items,omitempty"`

	// Unsupported captures anything outside the subset so parsing can refuse it
	// by name instead of ignoring it.
	Unsupported map[string]any `yaml:",inline" json:"-"`
}

// UnmarshalYAML decodes the node as usual and, on top, records the order of
// the property keys, which the plain decode into a map would lose.
func (s *Schema) UnmarshalYAML(node *yaml.Node) error {
	type plain Schema
	if err := node.Decode((*plain)(s)); err != nil {
		return err
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value != "properties" || node.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		props := node.Content[i+1]
		for j := 0; j < len(props.Content); j += 2 {
			s.Order = append(s.Order, props.Content[j].Value)
		}
	}
	return nil
}

var supportedTypes = map[string]bool{
	"object": true, "array": true, "string": true, "integer": true,
	"number": true, "boolean": true,
}

// check walks the schema itself, rejecting anything the validator would not
// actually enforce.
func (s *Schema) check(path string) error {
	if s == nil {
		return nil
	}
	for k := range s.Unsupported {
		return fmt.Errorf("config_schema%s: unsupported keyword %q — the connector validates a "+
			"documented subset of JSON Schema and refuses what it cannot enforce", path, k)
	}
	if s.Type == "" {
		return fmt.Errorf("config_schema%s: type is required", path)
	}
	if !supportedTypes[s.Type] {
		return fmt.Errorf("config_schema%s: unsupported type %q", path, s.Type)
	}
	if s.Pattern != "" {
		if _, err := regexp.Compile(s.Pattern); err != nil {
			return fmt.Errorf("config_schema%s: pattern does not compile: %w", path, err)
		}
	}
	if s.Default != nil {
		if s.Type == "object" || s.Type == "array" {
			return fmt.Errorf("config_schema%s: a default is allowed only on a scalar setting", path)
		}
		// A default the setting's own rules would refuse is a form that fails the
		// moment it is submitted untouched.
		if err := s.Validate(strings.TrimPrefix(path, "."), s.Default); err != nil {
			return fmt.Errorf("config_schema%s: the default does not satisfy the setting: %w", path, err)
		}
	}
	for name, sub := range s.Properties {
		if err := sub.check(path + "." + name); err != nil {
			return err
		}
	}
	return s.Items.check(path + "[]")
}

// Validate reports the first problem with a value, naming the field so the
// message can go straight into the setup form.
func (s *Schema) Validate(path string, v any) error {
	if s == nil {
		return nil
	}
	switch s.Type {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", label(path))
		}
		for _, req := range s.Required {
			if _, present := m[req]; !present {
				return fmt.Errorf("%s is required", label(join(path, req)))
			}
		}
		// Sorted so the operator gets the same message every time rather than
		// whichever field Go's map iteration happened to reach first.
		names := make([]string, 0, len(m))
		for name := range m {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			val := m[name]
			sub, known := s.Properties[name]
			if !known {
				return fmt.Errorf("%s is not a setting this plugin understands", label(join(path, name)))
			}
			if err := sub.Validate(join(path, name), val); err != nil {
				return err
			}
		}
	case "array":
		items, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s must be a list", label(path))
		}
		for i, item := range items {
			if err := s.Items.Validate(fmt.Sprintf("%s[%d]", path, i), item); err != nil {
				return err
			}
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s must be text", label(path))
		}
		if s.MinLength != nil && len(str) < *s.MinLength {
			return fmt.Errorf("%s must be at least %d characters", label(path), *s.MinLength)
		}
		if s.Pattern != "" {
			re, err := regexp.Compile(s.Pattern)
			if err != nil || !re.MatchString(str) {
				return fmt.Errorf("%s does not match the expected form", label(path))
			}
		}
		if len(s.Enum) > 0 && !contains(s.Enum, str) {
			return fmt.Errorf("%s must be one of %s", label(path), strings.Join(s.Enum, ", "))
		}
		if s.Format == "uri" {
			u, err := url.Parse(str)
			if err != nil || u.Scheme == "" || u.Host == "" {
				return fmt.Errorf("%s must be a full URL including https://", label(path))
			}
		}
	case "integer", "number":
		f, ok := toFloat(v)
		if !ok {
			return fmt.Errorf("%s must be a number", label(path))
		}
		if s.Minimum != nil && f < *s.Minimum {
			return fmt.Errorf("%s must be at least %v", label(path), *s.Minimum)
		}
		if s.Maximum != nil && f > *s.Maximum {
			return fmt.Errorf("%s must be at most %v", label(path), *s.Maximum)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%s must be true or false", label(path))
		}
	}
	return nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func label(path string) string {
	if path == "" {
		return "the settings"
	}
	return path
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
