package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

const goodManifest = `
name: jira
version: 3.2.0
host_api: 1
publisher: buff
summary: Jira Data Center and Server
platforms: [linux-x86_64, linux-aarch64]
entry: main.py
webhook: wake
signature:
  header: X-Hub-Signature
  algo: hmac-sha256
credentials:
  - name: api
    kinds: [bearer, basic]
egress:
  - from_config: base_url
    methods: [GET, POST]
    path_prefixes: ["/rest/api/"]
config_schema:
  type: object
  required: [base_url, project_keys]
  properties:
    base_url:
      type: string
      format: uri
    project_keys:
      type: array
      items: {type: string, pattern: "^[A-Z][A-Z0-9]+$"}
    polling_seconds:
      type: integer
      minimum: 30
tools:
  - name: search_issues
    read_only: true
    summary: Search the customer's tracker
`

func TestParseManifestReadsTheContract(t *testing.T) {
	m, err := ParseManifest([]byte(goodManifest))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Name != "jira" || m.Version != "3.2.0" || m.HostAPI != 1 {
		t.Fatalf("identity = %+v", m)
	}
	if m.Webhook != WebhookWake {
		t.Errorf("webhook = %q", m.Webhook)
	}
	if len(m.Egress) != 1 || m.Egress[0].FromConfig != "base_url" {
		t.Fatalf("egress = %+v", m.Egress)
	}
	if len(m.Egress[0].PathPrefixes) != 1 {
		t.Error("path prefixes are part of the contract, not decoration")
	}
	if len(m.Tools) != 1 || !m.Tools[0].ReadOnly {
		t.Fatalf("tools = %+v", m.Tools)
	}
}

func TestManifestRejectsMissingIdentity(t *testing.T) {
	for _, tc := range []struct{ name, yaml, want string }{
		{"no name", "version: 1.0.0\nhost_api: 1\nentry: m.py\n", "name"},
		{"bad name", "name: Jira Plugin!\nversion: 1.0.0\nhost_api: 1\nentry: m.py\n", "name"},
		{"no version", "name: jira\nhost_api: 1\nentry: m.py\n", "version"},
		{"bad version", "name: jira\nversion: three\nhost_api: 1\nentry: m.py\n", "version"},
		{"no host_api", "name: jira\nversion: 1.0.0\nentry: m.py\n", "host_api"},
		{"no entry", "name: jira\nversion: 1.0.0\nhost_api: 1\n", "entry"},
		{"escaping entry", "name: jira\nversion: 1.0.0\nhost_api: 1\nentry: ../../etc/passwd\n", "entry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// A tool with no explicit read-only claim is treated as writing. Getting this
// backwards would hand a reading pipeline stage a destructive tool.
func TestToolWithoutReadOnlyIsTreatedAsWriting(t *testing.T) {
	m, err := ParseManifest([]byte("name: x\nversion: 1.0.0\nhost_api: 1\nentry: m.py\ntools:\n  - name: create\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Tools[0].ReadOnly {
		t.Fatal("an unmarked tool must not be treated as read-only")
	}
}

// A picture tool says so in the manifest, so the catalogue can show it before
// installation; a tool that says nothing answers in text.
func TestToolVisionFlagIsCarriedAndDefaultsToText(t *testing.T) {
	m, err := ParseManifest([]byte("name: x\nversion: 1.0.0\nhost_api: 1\nentry: m.py\ntools:\n" +
		"  - name: render\n    read_only: true\n    vision: true\n  - name: search\n    read_only: true\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !m.Tools[0].Vision {
		t.Fatal("render answers with a picture and must say so")
	}
	if m.Tools[1].Vision {
		t.Fatal("an unmarked tool answers in text")
	}
}

// Egress is a RULE, not a list of addresses: the host is whatever the operator
// puts in that config field. A rule pointing at a field the schema does not
// define is a publishing mistake we must catch, not a runtime surprise.
func TestEgressMustReferenceARealConfigField(t *testing.T) {
	y := `
name: x
version: 1.0.0
host_api: 1
entry: m.py
egress:
  - from_config: nope
config_schema:
  type: object
  properties:
    base_url: {type: string}
`
	_, err := ParseManifest([]byte(y))
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v, want a complaint about the unknown field", err)
	}
}

func TestUnknownWebhookModeIsRejected(t *testing.T) {
	_, err := ParseManifest([]byte("name: x\nversion: 1.0.0\nhost_api: 1\nentry: m.py\nwebhook: shout\n"))
	if err == nil || !strings.Contains(err.Error(), "webhook") {
		t.Fatalf("err = %v", err)
	}
}

// The schema in the manifest drives the setup form AND validates what the
// operator typed. It is a deliberately small subset of JSON Schema so the
// connector keeps its two dependencies; the publisher validator enforces the
// same subset, so publisher and connector cannot drift.
func TestConfigValidationAcceptsGoodValues(t *testing.T) {
	m, _ := ParseManifest([]byte(goodManifest))
	err := m.ValidateConfig(map[string]any{
		"base_url":        "https://jira.example.com",
		"project_keys":    []any{"PROJ", "OPS2"},
		"polling_seconds": 60,
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestConfigValidationRejectsBadValues(t *testing.T) {
	m, _ := ParseManifest([]byte(goodManifest))
	for _, tc := range []struct {
		name string
		cfg  map[string]any
		want string
	}{
		{"missing required", map[string]any{"base_url": "https://x"}, "project_keys"},
		{"wrong type", map[string]any{"base_url": 5, "project_keys": []any{"PROJ"}}, "base_url"},
		{"bad uri", map[string]any{"base_url": "not a url", "project_keys": []any{"PROJ"}}, "base_url"},
		{"pattern", map[string]any{"base_url": "https://x", "project_keys": []any{"lower"}}, "project_keys"},
		{"below minimum", map[string]any{"base_url": "https://x", "project_keys": []any{"PROJ"}, "polling_seconds": 5}, "polling_seconds"},
		{"unknown field", map[string]any{"base_url": "https://x", "project_keys": []any{"PROJ"}, "typo": 1}, "typo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := m.ValidateConfig(tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one naming %q", err, tc.want)
			}
		})
	}
}

// The schema is a subset on purpose. A manifest using something outside it must
// be refused at parse time rather than silently unvalidated at runtime — an
// unenforced constraint is worse than no constraint, because everyone assumes
// it holds.
// A setting with a default is never "missing": the form shows it, the connector
// fills it before the plugin and the egress rules see the config, and the CLI
// path with no form lands on the same value.
func TestSchemaDefaultFillsAnUntouchedSetting(t *testing.T) {
	m, err := ParseManifest([]byte(`name: x
version: 1.0.0
host_api: 1
entry: m.py
egress:
  - from_config: base_url
    methods: [GET]
    path_prefixes: ["/v1/"]
config_schema:
  type: object
  required: [base_url]
  properties:
    base_url:
      type: string
      format: uri
      default: https://api.example.com
    files:
      type: array
      items:
        type: string
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg := m.WithDefaults(map[string]any{"files": []any{"K1"}})
	if cfg["base_url"] != "https://api.example.com" {
		t.Fatalf("default not applied: %+v", cfg)
	}
	if err := m.ValidateConfig(map[string]any{"files": []any{"K1"}}); err != nil {
		t.Fatalf("a required setting with a default must validate untouched: %v", err)
	}
	// An empty string is "untouched" too — that is what a cleared form field sends.
	if m.WithDefaults(map[string]any{"base_url": ""})["base_url"] != "https://api.example.com" {
		t.Fatal("an empty field must take the default")
	}
	// The operator's own value wins.
	if m.WithDefaults(map[string]any{"base_url": "http://127.0.0.1:8806"})["base_url"] != "http://127.0.0.1:8806" {
		t.Fatal("a typed value must not be overwritten by the default")
	}
	eg, err := m.ResolveEgress(m.WithDefaults(map[string]any{}))
	if err != nil || len(eg) != 1 || eg[0].Host != "api.example.com" {
		t.Fatalf("egress must resolve from the default: %+v %v", eg, err)
	}
}

// A default the setting's own rules refuse is a form that fails untouched; a
// default on a list is a shape no form shows. Both are refused at parse time.
func TestSchemaRefusesADefaultThatBreaksItsOwnRules(t *testing.T) {
	for _, tc := range []struct{ name, schema string }{
		{"not a uri", "    base_url:\n      type: string\n      format: uri\n      default: nowhere\n"},
		{"on a list", "    files:\n      type: array\n      default: []\n      items:\n        type: string\n"},
		{"wrong type", "    limit:\n      type: integer\n      default: ten\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifest([]byte("name: x\nversion: 1.0.0\nhost_api: 1\nentry: m.py\n" +
				"config_schema:\n  type: object\n  properties:\n" + tc.schema))
			if err == nil || !strings.Contains(err.Error(), "default") {
				t.Fatalf("err = %v, want a refusal naming the default", err)
			}
		})
	}
}

func TestSchemaFeaturesOutsideTheSubsetAreRefused(t *testing.T) {
	y := `
name: x
version: 1.0.0
host_api: 1
entry: m.py
config_schema:
  type: object
  properties:
    weird: {allOf: [{type: string}]}
`
	_, err := ParseManifest([]byte(y))
	if err == nil || !strings.Contains(err.Error(), "allOf") {
		t.Fatalf("err = %v, want a refusal naming the unsupported keyword", err)
	}
}

// Egress rules resolve against the operator's actual configuration: this is what
// turns "the host you type" into a concrete allowlist on the screen and in the
// HTTP guard.
func TestEgressResolvesToConcreteHosts(t *testing.T) {
	m, _ := ParseManifest([]byte(goodManifest))
	hosts, err := m.ResolveEgress(map[string]any{"base_url": "https://jira.example.com:8443/jira"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(hosts) != 1 || hosts[0].Host != "jira.example.com:8443" {
		t.Fatalf("hosts = %+v", hosts)
	}
	// The address carries a context path, so the prefix moves under it: a Jira
	// at /jira answers at /jira/rest/api/…, and the guard has to allow THAT.
	// This test used to expect the bare prefix, which is the defect a Cloud run
	// finally made visible.
	if len(hosts[0].Methods) != 2 || hosts[0].PathPrefixes[0] != "/jira/rest/api/" {
		t.Fatalf("rule not carried through: %+v", hosts[0])
	}
}

// The manifest crosses into the setup page as JSON, and the page builds the
// settings form from it. Go's default field names would arrive as Name/Kinds
// while the page reads name/kinds — the credential field then silently fails to
// render, and an operator cannot enter the token at all.
func TestManifestCrossesToTheUIWithTheNamesTheUIReads(t *testing.T) {
	m, err := ParseManifest([]byte(goodManifest))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	b, err := json.Marshal(map[string]any{
		"schema": m.Schema, "credentials": m.Creds, "tools": m.Tools, "egress": m.Egress,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{
		`"name":"api"`, `"kinds":["bearer","basic"]`,
		`"read_only":true`, `"from_config":"base_url"`,
		`"type":"object"`, `"format":"uri"`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the UI reads %s, which is missing from:\n%s", want, b)
		}
	}
}

// Both callback modes are real. The connector owns the listener either way; what
// differs is whether the validated body is handed on.
func TestWebhookModes(t *testing.T) {
	for _, mode := range []string{"none", "wake", "deliver"} {
		if _, err := ParseManifest([]byte(manifestWith(mode))); err != nil {
			t.Errorf("webhook %q was refused: %v", mode, err)
		}
	}
	if _, err := ParseManifest([]byte(manifestWith("shout"))); err == nil {
		t.Error("an unknown webhook mode was accepted")
	}
}

func manifestWith(mode string) string {
	return `
name: demo
version: 1.0.0
host_api: 1
summary: demo
publisher: buff
entry: main.py
webhook: ` + mode + `
`
}

// An address with a PATH is the normal case, not an exception: Confluence Cloud
// lives at https://site.atlassian.net/wiki, and a self-hosted Jira or Confluence
// behind a context path answers at /jira or /confluence. The manifest declares
// its prefixes relative to the product ("/rest/"), so the resolved rule has to
// carry the operator's path with them — otherwise every call is refused as
// "outside what this instance may call", which is exactly what happened.
func TestEgressPrefixesFollowTheConfiguredPath(t *testing.T) {
	m := &Manifest{Egress: []EgressRule{{
		FromConfig: "base_url", Methods: []string{"GET", "POST"},
		PathPrefixes: []string{"/rest/"},
	}}}

	cloud, err := m.ResolveEgress(map[string]any{"base_url": "https://acme.atlassian.net/wiki"})
	if err != nil {
		t.Fatal(err)
	}
	if got := cloud[0].PathPrefixes; len(got) != 1 || got[0] != "/wiki/rest/" {
		t.Errorf("cloud prefixes = %v", got)
	}
	if cloud[0].Host != "acme.atlassian.net" {
		t.Errorf("host = %q", cloud[0].Host)
	}

	// A trailing slash on the address must not double the one in the prefix.
	ctx, err := m.ResolveEgress(map[string]any{"base_url": "https://jira.corp.local/jira/"})
	if err != nil {
		t.Fatal(err)
	}
	if got := ctx[0].PathPrefixes; got[0] != "/jira/rest/" {
		t.Errorf("context-path prefixes = %v", got)
	}

	// An address with no path of its own is left exactly as it was.
	plain, err := m.ResolveEgress(map[string]any{"base_url": "https://wiki.corp.local"})
	if err != nil {
		t.Fatal(err)
	}
	if got := plain[0].PathPrefixes; got[0] != "/rest/" {
		t.Errorf("plain prefixes = %v", got)
	}
}

// A web rule names no host: the operator's lists ARE the rule. The manifest
// says which settings hold them, and every one of those must be a list the
// form can render.
const webManifest = `
name: web
version: 1.0.0
host_api: 2
entry: main.py
egress:
  - from_config: searxng_base
    methods: [GET]
    path_prefixes: ["/search"]
    optional: true
  - web:
      allow_from: allow
      deny_from: deny
      types_from: content_types
    methods: [GET]
config_schema:
  type: object
  properties:
    searxng_base: {type: string, format: uri}
    allow: {type: array, items: {type: string}}
    deny: {type: array, items: {type: string}}
    content_types: {type: array, items: {type: string}}
tools:
  - name: web_fetch
    read_only: true
    summary: Read a page
`

func TestWebEgressResolvesToAPolicyNotAHost(t *testing.T) {
	m, err := ParseManifest([]byte(webManifest))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rules, err := m.ResolveEgress(map[string]any{
		"allow": []any{"docs.python.org", "example.com/blog"},
		"deny":  []any{},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// The optional searxng rule is absent because its field is empty — not an
	// error, and not a rule either.
	if len(rules) != 1 || rules[0].Web == nil || rules[0].Host != "" {
		t.Fatalf("rules = %+v", rules)
	}
	if len(rules[0].Web.Allow) != 2 || rules[0].Methods[0] != "GET" {
		t.Fatalf("policy not carried: %+v", rules[0].Web)
	}
	if got := rules[0].Label(); got != "интернет: docs.python.org, example.com/blog" {
		t.Fatalf("label = %q", got)
	}
	// With the optional field filled in, the fixed rule appears beside the web one.
	rules, err = m.ResolveEgress(map[string]any{"searxng_base": "https://search.corp.example"})
	if err != nil || len(rules) != 2 || rules[0].Host != "search.corp.example" || rules[1].Web == nil {
		t.Fatalf("rules = %+v, %v", rules, err)
	}
	if rules[1].Label() != "интернет" {
		t.Fatalf("an empty policy is the open internet: %q", rules[1].Label())
	}
}

func TestWebEgressNamesTheBadEntryAndTheField(t *testing.T) {
	m, _ := ParseManifest([]byte(webManifest))
	_, err := m.ResolveEgress(map[string]any{"deny": []any{"https://pastebin.com"}})
	if err == nil || !strings.Contains(err.Error(), "deny") || !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("err = %v, want the field and the reason", err)
	}
	// The form validates the same way, so the operator reads the reason while
	// the field is still in front of them — not as a failed start later on.
	if err := m.ValidateConfig(map[string]any{"deny": []any{"https://pastebin.com"}}); err == nil ||
		!strings.Contains(err.Error(), "deny") {
		t.Fatalf("ValidateConfig = %v, want the bad entry named", err)
	}
	if err := m.ValidateConfig(map[string]any{"allow": []any{"docs.python.org"}}); err != nil {
		t.Fatalf("a good list must pass: %v", err)
	}
	_, err = m.ResolveEgress(map[string]any{"content_types": []any{"html"}})
	if err == nil || !strings.Contains(err.Error(), "content_types") {
		t.Fatalf("err = %v", err)
	}
}

func TestWebRuleMustPointAtListSettings(t *testing.T) {
	y := `
name: web
version: 1.0.0
host_api: 2
entry: main.py
egress:
  - web: {allow_from: allow}
config_schema:
  type: object
  properties:
    allow: {type: string}
`
	if _, err := ParseManifest([]byte(y)); err == nil || !strings.Contains(err.Error(), "allow") {
		t.Fatalf("a web list field that is not a list must be refused: %v", err)
	}
	both := `
name: web
version: 1.0.0
host_api: 2
entry: main.py
egress:
  - from_config: base
    web: {}
config_schema:
  type: object
  properties:
    base: {type: string}
`
	if _, err := ParseManifest([]byte(both)); err == nil {
		t.Fatal("a rule is either a configured host or the web, never both")
	}
}

// A `password` credential is the one kind the host HANDS to the plugin instead
// of attaching: a protocol the host does not speak (SMB) needs the secret in the
// plugin's own client. The manifest says so, which is what the catalogue shows.
func TestPasswordKindIsAcceptedAndCarriedToTheUI(t *testing.T) {
	m, err := ParseManifest([]byte(`
name: files
version: 1.0.0
host_api: 5
publisher: buff
summary: files on disk
platforms: [linux-x86_64]
entry: main.py
credentials:
  - name: share
    kinds: [password]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(m.Creds) != 1 || m.Creds[0].Kinds[0] != KindPassword {
		t.Fatalf("creds = %+v", m.Creds)
	}
	if !Handed(KindPassword) || Handed("basic") || Handed("bearer") {
		t.Fatal("password is the one handed kind")
	}
}

// A plugin that opens its own connections (SMB to the shares the operator
// names) declares that as a network rule pointing at a LIST setting: the
// consent screen names the field, so the boundary is visible before install.
func TestNetworkRuleMustPointAtAListSetting(t *testing.T) {
	base := `
name: files
version: 1.0.0
host_api: 5
publisher: buff
summary: files on disk
platforms: [linux-x86_64]
entry: main.py
config_schema:
  type: object
  properties:
    roots:
      type: array
      items: {type: string}
    note:
      type: string
`
	m, err := ParseManifest([]byte(base + `
network:
  - protocol: smb
    hosts_from: roots
    ports: [445]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(m.Network) != 1 || m.Network[0].Protocol != "smb" || m.Network[0].HostsFrom != "roots" || m.Network[0].Ports[0] != 445 {
		t.Fatalf("network = %+v", m.Network)
	}
	for name, rule := range map[string]string{
		"no protocol":   "network:\n  - hosts_from: roots\n    ports: [445]\n",
		"unknown field": "network:\n  - protocol: smb\n    hosts_from: shares\n    ports: [445]\n",
		"not a list":    "network:\n  - protocol: smb\n    hosts_from: note\n    ports: [445]\n",
		"no ports":      "network:\n  - protocol: smb\n    hosts_from: roots\n",
	} {
		if _, err := ParseManifest([]byte(base + rule)); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
	b, _ := json.Marshal(m.Network)
	for _, want := range []string{`"protocol":"smb"`, `"hosts_from":"roots"`, `"ports":[445]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the UI reads %s, missing from %s", want, b)
		}
	}
}

// The connections a plugin opens itself are listed on the report by host, the
// way buff/http hosts are: the operator sees "smb://nas.corp:445", not a field
// name. Hosts come out of the list setting, one per distinct server; a local
// path names no host and is skipped.
func TestNetworkResolvesToTheServersNamedInTheSetting(t *testing.T) {
	m, err := ParseManifest([]byte(`
name: files
version: 1.0.0
host_api: 5
publisher: buff
summary: files on disk
platforms: [linux-x86_64]
entry: main.py
config_schema:
  type: object
  properties:
    roots:
      type: array
      items: {type: string}
network:
  - protocol: smb
    hosts_from: roots
    ports: [445]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := m.ResolveNetwork(map[string]any{"roots": []any{
		`\\nas.corp\docs\specs`, "/srv/docs", `\\nas.corp\hr`, `smb://files.corp/share`, `//10.0.0.7/public`,
	}})
	want := []string{"smb://nas.corp:445", "smb://files.corp:445", "smb://10.0.0.7:445"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// The settings form shows fields in the order the manifest WRITES them — the
// author put the folders first and the exclusions after for a reason. A Go
// map forgets that order, so the schema records it as it is parsed and hands
// it to the page.
func TestSchemaKeepsThePropertyOrderTheManifestWrote(t *testing.T) {
	m, err := ParseManifest([]byte(`
name: files
version: 1.0.0
host_api: 5
publisher: buff
summary: files on disk
platforms: [linux-x86_64]
entry: main.py
config_schema:
  type: object
  properties:
    roots:
      type: array
      items: {type: string}
    excludes:
      type: array
      items: {type: string}
    alias:
      type: string
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := strings.Join(m.Schema.Order, ","); got != "roots,excludes,alias" {
		t.Fatalf("order = %q", got)
	}
	b, _ := json.Marshal(m.Schema)
	if !strings.Contains(string(b), `"order":["roots","excludes","alias"]`) {
		t.Fatalf("the page reads the order from JSON, missing in %s", b)
	}
}
