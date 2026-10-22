package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// The command line is not a lesser surface. A connector on a headless server is
// the normal case, not the exception, so everything the web page can do to a
// plugin instance is available here too — and scriptable, because an operator
// who manages this machine with Ansible should not have to click.
//
// It edits the same file the page edits, under the same lock and the same
// validation. There is no CLI-only setting and no page-only setting.

func cmdCatalog(args []string) {
	// Same ordering rule as `instance`: the verb, then flags.
	var verb, name string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		verb, args = args[0], args[1:]
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			name, args = args[0], args[1:]
		}
	}
	fs := flag.NewFlagSet("catalog", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to connector.yaml")
	_ = fs.Parse(args)
	rest := []string{}
	if verb != "" {
		rest = append(rest, verb)
	}
	if name != "" {
		rest = append(rest, name)
	}

	client := catalogClient(*cfgPath)
	ctx := context.Background()
	idx, err := client.Fetch(ctx)
	if err != nil {
		if cached, cErr := client.Cached(); cErr == nil {
			fmt.Fprintf(os.Stderr, "catalogue unreachable (%v) — showing the last one read\n", err)
			idx = cached
		} else {
			fatal("catalogue: %v", err)
		}
	}
	if len(rest) > 1 && rest[0] == "show" {
		for _, e := range idx.Plugins {
			if e.Name != rest[1] {
				continue
			}
			fmt.Printf("%s — %s\nиздатель: %s\n", e.Name, e.Summary, e.Publisher)
			for _, v := range e.Versions {
				fmt.Printf("\n  версия %s (host API %d)%s\n", v.Version, v.HostAPI, statusSuffix(v.Status, v.Reason))
				fmt.Printf("    выход наружу: по полям %s\n", strings.Join(v.EgressFrom, ", "))
				fmt.Printf("    учётные данные: %s\n", orNone(strings.Join(v.Creds, ", ")))
				fmt.Printf("    вебхуки: %s\n", orNone(v.Webhook))
				for _, t := range v.Tools {
					ro := ""
					if t.ReadOnly {
						ro = " [только чтение]"
					}
					fmt.Printf("    инструмент %s%s — %s\n", t.Name, ro, t.Summary)
				}
			}
			return
		}
		fatal("в каталоге нет плагина %q", rest[1])
	}

	for _, e := range idx.Plugins {
		v, ok := e.LatestCompatible(plugin.SupportedHostAPI)
		latest := "нет совместимой версии"
		if ok {
			latest = v.Version
		}
		fmt.Printf("%-14s %-10s %s\n", e.Name, latest, e.Summary)
	}
}

func cmdInstall(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fatal("usage: connector install <plugin>[@version] --config <path>")
	}
	ref, args := args[0], args[1:]
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to connector.yaml")
	_ = fs.Parse(args)
	name, version, _ := strings.Cut(ref, "@")

	cfg := mustLoadLenient(*cfgPath)
	client := catalogClient(*cfgPath)
	ctx := context.Background()
	idx, err := client.Fetch(ctx)
	if err != nil {
		fatal("catalogue: %v", err)
	}
	if version == "" {
		for _, e := range idx.Plugins {
			if e.Name == name {
				v, ok := e.LatestCompatible(plugin.SupportedHostAPI)
				if !ok {
					fatal("нет версии %s, совместимой с этой сборкой коннектора", name)
				}
				version = v.Version
			}
		}
	}
	entry, v, ok := idx.Find(name, version)
	if !ok {
		fatal("в каталоге нет %s %s", name, version)
	}
	bundle, sig, err := client.Download(ctx, entry.Name, v)
	if err != nil {
		fatal("download: %v", err)
	}
	store := plugin.NewStore(cfg.StateDir, plugin.TrustedKeys, nil)
	in, err := store.Install(bundle, sig)
	if err != nil {
		fatal("install: %v", err)
	}
	if err := store.Record(in); err != nil {
		fatal("record: %v", err)
	}
	fmt.Printf("установлен %s %s (%s)\n", in.Manifest.Name, in.Manifest.Version, in.Digest[:12])
}

func cmdInstance(args []string) {
	// The verb comes first, then flags. Go's flag package stops at the first
	// non-flag argument, so parsing before taking the verb would swallow every
	// flag after it and report them as missing.
	if len(args) == 0 {
		fatal("usage: connector instance list|show|set|secret|signin|rm|enable|disable|check|log [ref] --config <path>")
	}
	verb, args := args[0], args[1:]
	// The optional reference (id or slug) comes before the flags too, for the
	// same reason.
	ref := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		ref, args = args[0], args[1:]
	}

	fs := flag.NewFlagSet("instance", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to connector.yaml")
	alias := fs.String("alias", "", "human name for this connection")
	devProject := fs.String("dev-project", "", "project the work lands in")
	pluginName := fs.String("plugin", "", "plugin name")
	version := fs.String("version", "", "plugin version to pin this instance to")
	set := multiFlag{}
	fs.Var(&set, "set", "setting, key=value (repeatable)")
	poll := fs.Int("poll", -1, "how often to read this connection, in seconds (0 = the plugin's suggestion)")
	aiNotes := fs.String("ai-notes", "", "notes for the model about this connection (free text)")
	aiNotesFile := fs.String("ai-notes-file", "", "read the notes for the model from a file (\"-\" = stdin)")
	aiUnbounded := fs.String("ai-notes-unbounded", "", "true|false: lift the 2048-character limit on the notes, accepting that a long text can make the model worse")
	secret := fs.String("secret", "", "credential name to read from stdin")
	account := fs.String("account", "", "account the secret belongs to, for basic auth (email or login)")
	kind := fs.String("kind", "", "how the secret authenticates, when the plugin offers a choice (service_account, oauth2, …)")
	dryRun := fs.Bool("dry-run", false, "validate and show the difference without writing")
	addr := fs.String("addr", defaultAdminAddr, "local admin address, for live checks")
	_ = fs.Parse(args)

	switch verb {
	case "list":
		cfg := mustLoadLenient(*cfgPath)
		for _, p := range cfg.Plugins {
			state := "включено"
			if !p.IsEnabled() {
				state = "выключено"
			}
			fmt.Printf("%s  %-16s %-10s %-8s %s\n", p.ID, p.Slug, p.Plugin, p.Version, state)
			if p.Alias != "" {
				fmt.Printf("           %s\n", p.Alias)
			}
		}
	case "show":
		cfg := mustLoadLenient(*cfgPath)
		p := instanceByRef(cfg, ref)
		out, _ := json.MarshalIndent(redact(p), "", "  ")
		fmt.Println(string(out))
	case "set":
		instanceSet(*cfgPath, ref, *pluginName, *version, *alias, *devProject, set, *poll, *dryRun, func(inst *config.PluginInstance) error {
			return applyAINotes(inst, *aiNotes, *aiNotesFile, *aiUnbounded)
		})
	case "secret":
		instanceSecret(*cfgPath, ref, *secret, *account, *kind)
	case "signin":
		instanceSignIn(*addr, mustID(*cfgPath, ref), *secret)
	case "rm":
		instanceRemove(*cfgPath, ref, *dryRun)
	case "enable", "disable":
		instanceEnable(*cfgPath, ref, verb == "enable")
	case "check":
		liveCall(*addr, "/api/instances/check", map[string]any{"id": mustID(*cfgPath, ref)})
	case "log":
		liveGet(*addr, "/api/instances/log?id="+mustID(*cfgPath, ref))
	default:
		fatal("unknown instance command %q", verb)
	}
}

// instanceSet creates or edits one connection. The same validation the page
// runs, because it is the same code.
func instanceSet(cfgPath, ref, pluginName, version, alias, devProject string, set multiFlag, poll int, dryRun bool, edit func(*config.PluginInstance) error) {
	cfg := mustLoadLenient(cfgPath)
	store := plugin.NewStore(cfg.StateDir, plugin.TrustedKeys, nil)

	var inst *config.PluginInstance
	for i := range cfg.Plugins {
		if cfg.Plugins[i].ID == ref || cfg.Plugins[i].Slug == ref {
			inst = &cfg.Plugins[i]
		}
	}
	if inst == nil {
		if pluginName == "" || version == "" {
			fatal("a new instance needs --plugin and --version")
		}
		cfg.Plugins = append(cfg.Plugins, config.PluginInstance{ID: config.NewInstanceID()})
		inst = &cfg.Plugins[len(cfg.Plugins)-1]
		inst.Plugin, inst.Version = pluginName, version
	}
	if pluginName != "" {
		inst.Plugin = pluginName
	}
	if version != "" {
		inst.Version = version
	}
	if alias != "" {
		inst.Alias = alias
	}
	if devProject != "" {
		inst.DevProject = devProject
	}
	// -1 is "not given": zero is a real answer here (back to the plugin's
	// suggestion), so the flag cannot use it as its absent value.
	if poll >= 0 {
		inst.PollSeconds = poll
	}
	if err := edit(inst); err != nil {
		fatal("%v", err)
	}
	if inst.Slug == "" || alias != "" {
		name := inst.Alias
		if name == "" {
			name = inst.Plugin
		}
		taken := map[string]bool{}
		for _, other := range cfg.Plugins {
			if other.ID != inst.ID {
				taken[other.Slug] = true
			}
		}
		inst.Slug = config.UniqueSlugFor(name, taken)
	}

	installed, err := store.Resolve(inst.Plugin, inst.Version)
	if err != nil {
		fatal("%v", err)
	}
	if inst.Config == nil {
		inst.Config = map[string]any{}
	}
	schema := installed.Manifest.Schema
	for _, kv := range set {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			fatal("--set expects key=value, got %q", kv)
		}
		inst.Config[key] = coerce(schema, key, value)
	}
	if err := installed.Manifest.ValidateConfig(inst.Config); err != nil {
		fatal("%v", err)
	}
	// The platform's half of the checks — the ones the write would run anyway.
	// Asked here so a dry run answers the same as the real one.
	if err := cfg.CheckWritable(); err != nil {
		fatal("%v", err)
	}

	if dryRun {
		out, _ := json.MarshalIndent(redact(inst), "", "  ")
		fmt.Printf("проверено, записано НЕ будет:\n%s\n", out)
		return
	}
	fp, err := config.Fingerprint(cfgPath)
	if err != nil {
		fatal("%v", err)
	}
	if err := config.SaveIf(cfgPath, cfg, fp); err != nil {
		fatal("%v", err)
	}
	fmt.Printf("сохранено: %s (%s)\n", inst.Slug, inst.ID)
}

// applyAINotes edits the notes for the model from the flags that were GIVEN
// and leaves the rest alone, so `instance set --alias` does not wipe a note.
// The bound is lifted only by an explicit true: the warning the page shows
// before lifting it has no equivalent here, so the word has to be typed.
func applyAINotes(inst *config.PluginInstance, text, file, unbounded string) error {
	if text != "" && file != "" {
		return fmt.Errorf("--ai-notes and --ai-notes-file together: give the notes one way")
	}
	if text != "" {
		inst.AINotes = text
	}
	if file != "" {
		var b []byte
		var err error
		if file == "-" {
			b, err = io.ReadAll(os.Stdin)
		} else {
			b, err = os.ReadFile(file)
		}
		if err != nil {
			return fmt.Errorf("read --ai-notes-file: %w", err)
		}
		inst.AINotes = string(b)
	}
	switch unbounded {
	case "":
	case "true":
		inst.AINotesUnbounded = true
	case "false":
		inst.AINotesUnbounded = false
	default:
		return fmt.Errorf("--ai-notes-unbounded expects true or false, got %q", unbounded)
	}
	return nil
}

// instanceSecret reads a credential from stdin so it never appears in the shell
// history or in the process list.
// credentialSpec reads how a plugin says its tracker authenticates. An unknown
// credential (or a plugin whose bundle is not on disk) names nothing, and the
// caller falls back to a bearer token — which is what an undeclared credential
// has always meant.
func credentialSpec(man *plugin.Manifest, name string) (kinds []string, header string) {
	if man == nil {
		return nil, ""
	}
	for _, c := range man.Creds {
		if c.Name == name {
			return c.Kinds, c.Header
		}
	}
	return nil, ""
}

func instanceSecret(cfgPath, ref, name, account, kind string) {
	if name == "" {
		fatal("--secret <name> is required")
	}
	cfg := mustLoadLenient(cfgPath)
	inst := instanceByRef(cfg, ref)

	fmt.Fprintln(os.Stderr, "введите значение и нажмите Ctrl-D:")
	value, err := io.ReadAll(bufio.NewReader(os.Stdin))
	if err != nil {
		fatal("read: %v", err)
	}
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" {
		fatal("пустое значение")
	}
	dir := filepath.Join(cfg.StateDir, "secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fatal("%v", err)
	}
	file := filepath.Join(dir, fmt.Sprintf("%s-%s-%s.token", inst.Plugin, inst.ID, name))
	if err := os.WriteFile(file, []byte(trimmed), 0o600); err != nil {
		fatal("%v", err)
	}
	// HOW the tracker authenticates is the plugin's declaration, not a guess:
	// Redmine takes its key only in a header of its own, Jira Cloud takes an
	// email with the token. The same rule the local page uses decides it.
	var man *plugin.Manifest
	if in, err := plugin.NewStore(cfg.StateDir, plugin.TrustedKeys, nil).
		Resolve(inst.Plugin, inst.Version); err == nil {
		man = in.Manifest
	}
	kinds, header := credentialSpec(man, name)
	if kind != "" && !containsKind(kinds, kind) {
		fatal("плагин не предлагает способ входа %q для %q (есть: %s)", kind, name, strings.Join(kinds, ", "))
	}
	cred := config.CredentialOfKind(name, kind, kinds, header, account, file)
	found := false
	for i := range inst.Credentials {
		if inst.Credentials[i].Name == name {
			inst.Credentials[i], found = cred, true
		}
	}
	if !found {
		inst.Credentials = append(inst.Credentials, cred)
	}
	fp, _ := config.Fingerprint(cfgPath)
	if err := config.SaveIf(cfgPath, cfg, fp); err != nil {
		fatal("%v", err)
	}
	fmt.Printf("записано в %s (права 0600), в конфиг попала только ссылка\n", file)
	fmt.Printf("вид доступа: %s%s\n", cred.Kind,
		map[bool]string{true: ", учётная запись " + cred.Username}[cred.Username != ""])
}

func containsKind(kinds []string, kind string) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// instanceSignIn signs an oauth2 credential in from a terminal: the running
// connector mints the consent address, the operator opens it wherever they
// have a browser, and pastes the address the source returned them to.
func instanceSignIn(addr, id, name string) {
	if name == "" {
		fatal("--secret <name> is required: which credential to sign in")
	}
	var started struct {
		OK    bool   `json:"ok"`
		URL   string `json:"url"`
		Error string `json:"error"`
	}
	liveJSON(addr, "/api/instances/oauth/start", map[string]any{"id": id, "name": name}, &started)
	if !started.OK {
		fatal("%s", started.Error)
	}
	fmt.Println("откройте в браузере и войдите:")
	fmt.Println("  " + started.URL)
	fmt.Fprintln(os.Stderr, "затем вставьте сюда адрес страницы, на которую вернул источник (или код из него), и нажмите Enter:")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		fatal("read: %v", err)
	}
	var done struct {
		OK      bool   `json:"ok"`
		Account string `json:"account"`
		Error   string `json:"error"`
	}
	liveJSON(addr, "/api/instances/oauth/finish", map[string]any{"id": id, "name": name, "pasted": strings.TrimSpace(line)}, &done)
	if !done.OK {
		fatal("%s", done.Error)
	}
	fmt.Printf("вход выполнен%s\n", map[bool]string{true: " как " + done.Account}[done.Account != ""])
}

func instanceRemove(cfgPath, ref string, dryRun bool) {
	cfg := mustLoadLenient(cfgPath)
	inst := instanceByRef(cfg, ref)
	if dryRun {
		fmt.Printf("будет удалено: %s (%s)\n", inst.Slug, inst.ID)
		return
	}
	kept := cfg.Plugins[:0]
	for _, p := range cfg.Plugins {
		if p.ID != inst.ID {
			kept = append(kept, p)
		}
	}
	cfg.Plugins = kept
	fp, _ := config.Fingerprint(cfgPath)
	if err := config.SaveIf(cfgPath, cfg, fp); err != nil {
		fatal("%v", err)
	}
	fmt.Printf("удалено: %s\n", inst.Slug)
}

func instanceEnable(cfgPath, ref string, on bool) {
	cfg := mustLoadLenient(cfgPath)
	inst := instanceByRef(cfg, ref)
	inst.Enabled = &on
	fp, _ := config.Fingerprint(cfgPath)
	if err := config.SaveIf(cfgPath, cfg, fp); err != nil {
		fatal("%v", err)
	}
	fmt.Printf("%s: %v\n", inst.Slug, map[bool]string{true: "включено", false: "выключено"}[on])
}

// ---- helpers ----------------------------------------------------------

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// coerce turns a command-line string into the type the schema asks for, so
// `--set polling_seconds=60` is a number and `--set project_keys=A,B` is a list.
func coerce(schema *plugin.Schema, key, value string) any {
	if schema == nil || schema.Properties[key] == nil {
		return value
	}
	switch schema.Properties[key].Type {
	case "integer", "number":
		if n, err := strconv.ParseFloat(value, 64); err == nil {
			return n
		}
	case "boolean":
		if b, err := strconv.ParseBool(value); err == nil {
			return b
		}
	case "array":
		parts := strings.Split(value, ",")
		out := make([]any, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return value
}

func instanceByRef(cfg *config.Config, ref string) *config.PluginInstance {
	if ref == "" {
		fatal("which instance? give its id or slug (connector instance list)")
	}
	for i := range cfg.Plugins {
		if cfg.Plugins[i].ID == ref || cfg.Plugins[i].Slug == ref {
			return &cfg.Plugins[i]
		}
	}
	fatal("нет подключения %q", ref)
	return nil
}

func mustID(cfgPath, ref string) string { return instanceByRef(mustLoadLenient(cfgPath), ref).ID }

func mustLoadLenient(path string) *config.Config {
	if path == "" {
		fatal("--config is required")
	}
	cfg, err := config.LoadLenient(path)
	if err != nil {
		fatal("config: %v", err)
	}
	cfg.CompleteInstances()
	return cfg
}

// redact copies an instance for display with credential references kept and
// values impossible: the values are not in the config to begin with.
func redact(p *config.PluginInstance) map[string]any {
	creds := make([]map[string]string, 0, len(p.Credentials))
	for _, c := range p.Credentials {
		creds = append(creds, map[string]string{"name": c.Name, "kind": c.Kind, "token_file": c.TokenFile})
	}
	keys := make([]string, 0, len(p.Config))
	for k := range p.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	cfg := map[string]any{}
	for _, k := range keys {
		cfg[k] = p.Config[k]
	}
	return map[string]any{
		"id": p.ID, "plugin": p.Plugin, "version": p.Version, "alias": p.Alias,
		"slug": p.Slug, "dev_project": p.DevProject, "enabled": p.IsEnabled(),
		"config": cfg, "credentials": creds,
		"ai_notes": p.AINotes, "ai_notes_unbounded": p.AINotesUnbounded,
	}
}

// liveCall reaches the running agent's local surface for things that only make
// sense against a live process.
func liveCall(addr, path string, body map[string]any) {
	fmt.Println(string(livePost(addr, path, body)))
}

// liveJSON is liveCall for a caller that reads the answer.
func liveJSON(addr, path string, body map[string]any, out any) {
	if err := json.Unmarshal(livePost(addr, path, body), out); err != nil {
		fatal("connector answered something that is not JSON: %v", err)
	}
}

func livePost(addr, path string, body map[string]any) []byte {
	token := os.Getenv("BUFF_ADMIN_TOKEN")
	if token == "" {
		fatal("live checks need the admin token: set BUFF_ADMIN_TOKEN to the one the connector printed")
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", "http://"+addr+path, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Buff-Local", "1")
	req.Header.Set("X-Buff-Token", token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal("connector is not running on %s: %v", addr, err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return out
}

func liveGet(addr, path string) {
	token := os.Getenv("BUFF_ADMIN_TOKEN")
	req, _ := http.NewRequest("GET", "http://"+addr+path, nil)
	req.Header.Set("X-Buff-Token", token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal("connector is not running on %s: %v", addr, err)
	}
	defer res.Body.Close()
	var payload struct {
		Lines []plugin.LogLine `json:"lines"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		fatal("%v", err)
	}
	for _, l := range payload.Lines {
		fmt.Printf("%s  %s\n", l.At.Format("15:04:05"), l.Text)
	}
}

func statusSuffix(status, reason string) string {
	if status == "" {
		return ""
	}
	return fmt.Sprintf("  [%s: %s]", status, reason)
}

func orNone(s string) string {
	if s == "" {
		return "нет"
	}
	return s
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
