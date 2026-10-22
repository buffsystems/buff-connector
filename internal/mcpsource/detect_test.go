package mcpsource

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// An MCP Bundle says exactly how to run itself and what to ask the operator.
// Its word is taken as the launch, its user_config becomes the form, and the
// placeholders stay placeholders until the connector expands them at start.
func TestDetectReadsAnMCPBundle(t *testing.T) {
	dir := tree(t, map[string]string{"manifest.json": `{
  "manifest_version": "0.2", "name": "weather", "version": "0.3.0", "description": "Forecasts.",
  "server": {"type": "node", "entry_point": "server/index.js",
    "mcp_config": {"command": "node", "args": ["${__dirname}/server/index.js"],
      "env": {"API_KEY": "${user_config.api_key}", "UNITS": "${user_config.units}"}}},
  "user_config": {
    "api_key": {"type": "string", "title": "API key", "description": "From the vendor.", "sensitive": true, "required": true},
    "units": {"type": "string", "title": "Units", "default": "metric"}
  },
  "tools": [{"name": "forecast", "description": "Forecast for a city."}]
}`, "server/index.js": "// server"})
	l, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if l.Name != "weather" || l.Version != "0.3.0" || l.Runtime != "node" || l.Command != "node" {
		t.Fatalf("launch = %+v", l)
	}
	if len(l.Args) != 1 || l.Args[0] != "${__dirname}/server/index.js" {
		t.Fatalf("args = %v", l.Args)
	}
	if l.Env["API_KEY"] != "${user_config.api_key}" {
		t.Fatalf("env = %v", l.Env)
	}
	if len(l.Fields) != 2 || l.Fields[0].Key != "api_key" || !l.Fields[0].Sensitive || !l.Fields[0].Required ||
		l.Fields[1].Key != "units" || l.Fields[1].Default != "metric" {
		t.Fatalf("fields = %+v", l.Fields)
	}
	if l.Deps != "" {
		t.Error("a bundle ships its own dependencies; nothing to install")
	}
	if len(l.Tools) != 1 || l.Tools[0] != "forecast" {
		t.Errorf("tools = %v", l.Tools)
	}
}

// A Node project runs its bin (or main) under node, after npm has installed
// what it declares — and after a build when the entry is generated.
func TestDetectReadsANodeProject(t *testing.T) {
	dir := tree(t, map[string]string{
		"package.json": `{"name": "@acme/weather-mcp", "version": "1.4.0", "description": "Weather",
  "bin": {"weather-mcp": "dist/index.js"}, "scripts": {"build": "tsc"}, "dependencies": {"@modelcontextprotocol/sdk": "^1.0.0"}}`,
		"package-lock.json": "{}",
		"src/index.ts":      "",
	})
	l, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if l.Runtime != "node" || l.Command != "node" || strings.Join(l.Args, " ") != "${__dirname}/dist/index.js" {
		t.Fatalf("launch = %+v", l)
	}
	if l.Deps != "npm" || !l.Build {
		t.Fatalf("dist/index.js does not exist yet: npm must install and build, got deps=%q build=%v", l.Deps, l.Build)
	}
	if l.Name != "@acme/weather-mcp" || l.Version != "1.4.0" {
		t.Errorf("name/version = %q %q", l.Name, l.Version)
	}

	plain := tree(t, map[string]string{
		"package.json": `{"name": "srv", "main": "index.js", "dependencies": {"x": "1"}}`,
		"index.js":     "",
	})
	l, err = Detect(plain)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(l.Args, " ") != "${__dirname}/index.js" || l.Build {
		t.Fatalf("main with an existing entry: %+v", l)
	}
}

// A Python project is installed into its own environment and run from there.
func TestDetectReadsAPythonProject(t *testing.T) {
	dir := tree(t, map[string]string{"pyproject.toml": `[project]
name = "weather-mcp"
version = "0.2.1"
dependencies = ["mcp>=1.0"]

[project.scripts]
weather-mcp = "weather_mcp.server:main"
`})
	l, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if l.Runtime != "python" || l.Deps != "pip" || l.Name != "weather-mcp" || l.Version != "0.2.1" {
		t.Fatalf("launch = %+v", l)
	}
	if l.Command != "${__dirname}/.venv/bin/weather-mcp" {
		t.Fatalf("command = %q", l.Command)
	}

	noScript := tree(t, map[string]string{"pyproject.toml": "[project]\nname = \"weather-mcp\"\n"})
	l, err = Detect(noScript)
	if err != nil {
		t.Fatal(err)
	}
	if l.Command != "${__dirname}/.venv/bin/python" || strings.Join(l.Args, " ") != "-m weather_mcp" {
		t.Fatalf("without a script the package itself runs: %+v", l)
	}
}

// A registry server.json names the package and the variables it needs; the
// launch still comes from the code beside it, and when there is no code the
// package is run from its registry.
func TestDetectReadsARegistryServerJSON(t *testing.T) {
	dir := tree(t, map[string]string{
		"server.json": `{"name": "io.github.acme/weather", "description": "Weather", "version": "2.0.0",
  "packages": [{"registryType": "npm", "identifier": "@acme/weather-mcp", "version": "2.0.0",
    "environmentVariables": [{"name": "API_KEY", "description": "vendor key", "isRequired": true, "isSecret": true}]}]}`,
	})
	l, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if l.Name != "io.github.acme/weather" || l.Version != "2.0.0" {
		t.Fatalf("launch = %+v", l)
	}
	if l.Command != "npx" || strings.Join(l.Args, " ") != "-y @acme/weather-mcp@2.0.0" {
		t.Fatalf("no code beside it: run the package, got %+v", l)
	}
	if len(l.Fields) != 1 || l.Fields[0].Key != "API_KEY" || !l.Fields[0].Sensitive || !l.Fields[0].Required {
		t.Fatalf("fields = %+v", l.Fields)
	}
	if l.Env["API_KEY"] != "${user_config.API_KEY}" {
		t.Fatalf("a declared variable must be wired to the form: %v", l.Env)
	}
}

// Nothing recognisable is not an error: the operator types the command.
func TestDetectSaysNothingWhenItKnowsNothing(t *testing.T) {
	l, err := Detect(tree(t, map[string]string{"README.md": "hi"}))
	if err != nil {
		t.Fatal(err)
	}
	if l.Runtime != "" || l.Command != "" {
		t.Fatalf("guessed: %+v", l)
	}
}

// Placeholders are expanded at start, from the directory and the operator's
// answers; one that nobody can answer is an error naming it, not an empty
// string handed to the server.
func TestExpandFillsPlaceholdersAndNamesTheMissing(t *testing.T) {
	vars := map[string]string{"__dirname": "/srv", "HOME": "/home/x", "user_config.api_key": "k1"}
	got, err := Expand("${__dirname}/dist/index.js --key ${user_config.api_key} ${HOME}", vars)
	if err != nil || got != "/srv/dist/index.js --key k1 /home/x" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := Expand("${user_config.units}", vars); err == nil || !strings.Contains(err.Error(), "units") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := Expand("a${/}b", map[string]string{"/": "/"}); got != "a/b" {
		t.Fatalf("got %q", got)
	}
}
