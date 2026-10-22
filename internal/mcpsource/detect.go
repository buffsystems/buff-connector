package mcpsource

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Launch is how to run the server found in a directory, in the words of its
// own packaging. Placeholders — ${__dirname}, ${user_config.KEY}, ${HOME} —
// are left in place: what they stand for is only known at start.
type Launch struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Description string            `json:"description"`
	Runtime     string            `json:"runtime"` // node, python, binary, or "" when unknown
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env"`
	Fields      []Field           `json:"fields"` // what to ask the operator
	Tools       []string          `json:"tools"`  // what the packaging claims, for the operator
	Deps        string            `json:"deps"`   // npm, pip, or "" — what Install runs
	Build       bool              `json:"build"`  // the entry is generated: build after installing
}

// Draft is what the local page shows before the operator saves: the launch
// as the packaging describes it, the log of the install, and the reason if
// this machine cannot run it.
type Draft struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	Origin    string    `json:"origin"`
	Launch    Launch    `json:"launch"`
	Needs     string    `json:"needs,omitempty"`
	Log       string    `json:"log,omitempty"`
	Toolchain Toolchain `json:"toolchain"`
}

// Field is one answer the operator gives. A sensitive one is a secret: it is
// stored the way credentials are and reaches the server through a placeholder.
type Field struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Sensitive   bool   `json:"sensitive"`
	Default     string `json:"default"`
}

// Detect reads the directory's own packaging, most specific first.
func Detect(dir string) (Launch, error) {
	if b, err := os.ReadFile(filepath.Join(dir, "manifest.json")); err == nil {
		return detectBundle(b)
	}
	var l Launch
	if b, err := os.ReadFile(filepath.Join(dir, "server.json")); err == nil {
		l = detectRegistry(b)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		return detectNode(dir, b, l)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "pyproject.toml")); err == nil {
		return detectPython(string(b), l)
	}
	return l, nil
}

// detectBundle reads an MCP Bundle (.mcpb / .dxt) manifest.
func detectBundle(raw []byte) (Launch, error) {
	var m struct {
		Name, Version, Description string
		Server                     struct {
			Type      string `json:"type"`
			MCPConfig struct {
				Command string            `json:"command"`
				Args    []string          `json:"args"`
				Env     map[string]string `json:"env"`
			} `json:"mcp_config"`
		} `json:"server"`
		UserConfig map[string]struct {
			Type, Title, Description string
			Required, Sensitive      bool
			Default                  any
		} `json:"user_config"`
		Tools []struct{ Name string } `json:"tools"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return Launch{}, fmt.Errorf("manifest.json: %w", err)
	}
	l := Launch{Name: m.Name, Version: m.Version, Description: m.Description, Runtime: m.Server.Type,
		Command: m.Server.MCPConfig.Command, Args: m.Server.MCPConfig.Args, Env: m.Server.MCPConfig.Env}
	keys := make([]string, 0, len(m.UserConfig))
	for k := range m.UserConfig {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f := m.UserConfig[k]
		l.Fields = append(l.Fields, Field{Key: k, Title: f.Title, Description: f.Description, Type: f.Type,
			Required: f.Required, Sensitive: f.Sensitive, Default: fmt.Sprint(orEmpty(f.Default))})
	}
	for _, t := range m.Tools {
		l.Tools = append(l.Tools, t.Name)
	}
	return l, nil
}

func orEmpty(v any) any {
	if v == nil {
		return ""
	}
	return v
}

// detectRegistry reads the MCP registry's server.json: the name, the package
// and the variables it needs. Both spellings of its keys are read — the schema
// changed case between revisions and both are in the wild.
func detectRegistry(raw []byte) Launch {
	var s struct {
		Name, Description, Version string
		Packages                   []struct {
			RegistryType string `json:"registryType"`
			RegistryName string `json:"registry_name"`
			Identifier   string `json:"identifier"`
			Name         string `json:"name"`
			Version      string `json:"version"`
			EnvVars      []struct {
				Name, Description string
				IsRequired        bool `json:"isRequired"`
				IsSecret          bool `json:"isSecret"`
				Required          bool `json:"is_required"`
				Secret            bool `json:"is_secret"`
			} `json:"environmentVariables"`
			EnvVarsSnake []struct {
				Name, Description string
				Required          bool `json:"is_required"`
				Secret            bool `json:"is_secret"`
			} `json:"environment_variables"`
		}
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return Launch{}
	}
	l := Launch{Name: s.Name, Version: s.Version, Description: s.Description, Env: map[string]string{}}
	if len(s.Packages) == 0 {
		return l
	}
	p := s.Packages[0]
	kind := p.RegistryType
	if kind == "" {
		kind = p.RegistryName
	}
	id := p.Identifier
	if id == "" {
		id = p.Name
	}
	if p.Version != "" {
		id += "@" + p.Version
	}
	switch kind {
	case "npm":
		l.Runtime, l.Command, l.Args = "node", "npx", []string{"-y", id}
	case "pypi":
		l.Runtime, l.Command, l.Args = "python", "uvx", []string{id}
	}
	for _, v := range p.EnvVars {
		l.Fields = append(l.Fields, Field{Key: v.Name, Title: v.Name, Description: v.Description, Type: "string",
			Required: v.IsRequired || v.Required, Sensitive: v.IsSecret || v.Secret})
		l.Env[v.Name] = "${user_config." + v.Name + "}"
	}
	for _, v := range p.EnvVarsSnake {
		l.Fields = append(l.Fields, Field{Key: v.Name, Title: v.Name, Description: v.Description, Type: "string",
			Required: v.Required, Sensitive: v.Secret})
		l.Env[v.Name] = "${user_config." + v.Name + "}"
	}
	return l
}

// detectNode reads package.json: the entry is bin, else main; a build script
// with a missing entry means the entry is generated.
func detectNode(dir string, raw []byte, base Launch) (Launch, error) {
	var p struct {
		Name, Version, Description string
		Bin                        json.RawMessage
		Main                       string
		Scripts                    map[string]string
		Dependencies               map[string]string
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return Launch{}, fmt.Errorf("package.json: %w", err)
	}
	l := base
	if l.Name == "" {
		l.Name, l.Version, l.Description = p.Name, p.Version, p.Description
	}
	entry := ""
	var binStr string
	var binMap map[string]string
	if json.Unmarshal(p.Bin, &binStr) == nil && binStr != "" {
		entry = binStr
	} else if json.Unmarshal(p.Bin, &binMap) == nil && len(binMap) > 0 {
		names := make([]string, 0, len(binMap))
		for n := range binMap {
			names = append(names, n)
		}
		sort.Strings(names)
		entry = binMap[names[0]]
	}
	if entry == "" {
		entry = p.Main
	}
	if entry == "" {
		for _, c := range []string{"dist/index.js", "build/index.js", "index.js"} {
			if _, err := os.Stat(filepath.Join(dir, c)); err == nil {
				entry = c
				break
			}
		}
	}
	if entry == "" {
		return l, nil // a Node package with no entry: the operator says what runs
	}
	l.Runtime, l.Command, l.Args = "node", "node", []string{"${__dirname}/" + filepath.ToSlash(filepath.Clean(entry))}
	if len(p.Dependencies) > 0 || p.Scripts["build"] != "" {
		l.Deps = "npm"
	}
	if _, err := os.Stat(filepath.Join(dir, entry)); err != nil && p.Scripts["build"] != "" {
		l.Build = true
	}
	return l, nil
}

var (
	tomlProject = regexp.MustCompile(`(?m)^\s*\[project\]\s*$`)
	tomlScripts = regexp.MustCompile(`(?m)^\s*\[project\.scripts\]\s*$`)
	tomlSection = regexp.MustCompile(`(?m)^\s*\[`)
	tomlKV      = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_\-]+)\s*=\s*"([^"]*)"`)
)

// detectPython reads pyproject.toml — only the [project] table and its scripts,
// which is all a launch needs. A full TOML parser is a dependency this binary
// does not carry for two keys.
func detectPython(raw string, base Launch) (Launch, error) {
	l := base
	project := tomlTable(raw, tomlProject)
	if l.Name == "" {
		l.Name, l.Version, l.Description = project["name"], project["version"], project["description"]
	}
	if project["name"] == "" && l.Name == "" {
		return l, nil
	}
	l.Runtime, l.Deps = "python", "pip"
	scripts := tomlTable(raw, tomlScripts)
	names := make([]string, 0, len(scripts))
	for n := range scripts {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > 0 {
		l.Command, l.Args = "${__dirname}/.venv/bin/"+names[0], nil
		return l, nil
	}
	module := strings.ReplaceAll(project["name"], "-", "_")
	l.Command, l.Args = "${__dirname}/.venv/bin/python", []string{"-m", module}
	return l, nil
}

// tomlTable returns the string keys of one table.
func tomlTable(raw string, header *regexp.Regexp) map[string]string {
	out := map[string]string{}
	loc := header.FindStringIndex(raw)
	if loc == nil {
		return out
	}
	body := raw[loc[1]:]
	if next := tomlSection.FindStringIndex(body); next != nil {
		body = body[:next[0]]
	}
	for _, m := range tomlKV.FindAllStringSubmatch(body, -1) {
		out[m[1]] = m[2]
	}
	return out
}
