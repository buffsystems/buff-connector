package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/mcpsource"
	"github.com/buffsystems/buff-connector/internal/plugin"
	"github.com/buffsystems/buff-connector/internal/problem"
)

// A custom MCP server (#351) is started the way a catalogue plugin is — its own
// user, its own directory, supervised, never switched off — with three
// differences that follow from nobody having signed it: there is no store to
// resolve it from and no integrity to check, there is no host gate because it
// speaks to the network itself, and what counts as read-only is the operator's
// mark rather than the server's claim.

// CodeDir is where a custom server's code lives when it came as an archive or
// a repository: beside the instances, keyed on the same GUID, owned by the same
// user so that installing dependencies happens as that user and nobody else.
func CodeDir(stateDir, id string) string { return filepath.Join(stateDir, "mcp", id) }

// Toolchain is what this machine offers a custom server, as the connection's
// user would see it. Resolved on demand: installing Node later needs no restart.
func (m *PluginManager) Toolchain() mcpsource.Toolchain {
	tc := mcpsource.FindToolchain(exec.LookPath, func(p string) bool { return reachableByOthers(p, modeOf) })
	// The interpreter the plugins run under serves a Python server too: it is
	// staged beside the state, where the connection's user can reach it.
	if tc.Python == "" {
		tc.Python = m.interpreter()
	}
	return tc
}

func (m *PluginManager) startCustom(p *config.PluginInstance, fp string, report plugin.InstanceReport, fail func(error)) {
	mcp := p.MCP
	report.Custom, report.Source = true, mcp.Source
	report.Plugin, report.Version = mcp.Name, mcp.Version
	if mcp.IsRemote() {
		m.startRemote(p, fp, report)
		return
	}

	uid, err := m.uids.For(p.ID)
	if err != nil {
		fail(err)
		return
	}
	dir, err := plugin.PrepareInstanceDir(m.stateDir, p.ID, uid, uid)
	if err != nil {
		fail(err)
		return
	}
	// The code's directory is the working directory when there is one — that is
	// where a relative entry point and node_modules resolve. A typed command
	// runs from the instance's own directory.
	cwd := dir
	if code := CodeDir(m.stateDir, p.ID); mcp.Source != config.MCPFromCommand {
		if st, err := os.Stat(code); err == nil && st.IsDir() {
			cwd = code
		}
	}

	vars := expansionVars(p, cwd, dir)
	expand := func(s string) (string, error) {
		out, err := mcpsource.Expand(s, vars)
		if err != nil {
			return "", problem.Wrap("command_value_missing", err, s)
		}
		return out, nil
	}
	command, err := expand(mcp.Command)
	if err != nil {
		fail(err)
		return
	}
	if command, err = mcpsource.ResolveCommand(command, m.Toolchain(), exec.LookPath); err != nil {
		fail(err)
		return
	}
	args := make([]string, 0, len(mcp.Args))
	for _, a := range mcp.Args {
		v, err := expand(a)
		if err != nil {
			fail(err)
			return
		}
		args = append(args, v)
	}

	env := []string{"HOME=" + dir, "TMPDIR=" + dir, "PATH=" + os.Getenv("PATH")}
	names := make([]string, 0, len(mcp.Env))
	for k := range mcp.Env {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		v, err := expand(mcp.Env[k])
		if err != nil {
			fail(err)
			return
		}
		env = append(env, k+"="+v)
	}
	// An env credential reaches the process under its own name, unasked; a
	// secret one only where the command or the env block spells it out.
	for _, c := range p.Credentials {
		if c.Kind == config.CredentialEnv {
			env = append(env, c.Name+"="+c.Token)
		}
	}

	inst := plugin.NewInstance(m.starter, plugin.InstanceConfig{
		GUID: p.ID, Slug: p.Slug, PlainMCP: true, ReadOnlyTools: mcp.ReadOnly,
		Spec: plugin.Spec{Path: command, Args: args, Dir: cwd, Env: env, UID: uid, GID: uid},
	}, nil)

	if err := inst.Start(m.base); err != nil {
		report.Status = string(inst.State().Status)
		report.LastError, report.Problem = err.Error(), problem.From(err)
	} else {
		report.Status = string(plugin.StatusRunning)
	}
	m.mu.Lock()
	delete(m.failed, p.ID)
	m.running[p.ID] = &runningInstance{inst: inst, fingerprint: fp, report: report}
	m.mu.Unlock()
}

// startRemote reaches a server already running somewhere. No process, no user,
// no directory: the operator's header travels on every request, and that is
// the whole relationship.
func (m *PluginManager) startRemote(p *config.PluginInstance, fp string, report plugin.InstanceReport) {
	headers := map[string]string{}
	for _, c := range p.Credentials {
		if c.Kind == "header" && c.Token != "" {
			name := c.Header
			if name == "" {
				name = "Authorization"
			}
			headers[name] = c.Token
		}
	}
	report.Egress = []string{p.MCP.URL}
	inst := plugin.NewInstance(m.starter, plugin.InstanceConfig{
		GUID: p.ID, Slug: p.Slug, PlainMCP: true, ReadOnlyTools: p.MCP.ReadOnly,
		Remote: &plugin.RemoteSpec{URL: p.MCP.URL, Headers: headers},
	}, nil)
	if err := inst.Start(m.base); err != nil {
		report.Status = string(inst.State().Status)
		report.LastError, report.Problem = err.Error(), problem.From(err)
	} else {
		report.Status = string(plugin.StatusRunning)
	}
	m.mu.Lock()
	delete(m.failed, p.ID)
	m.running[p.ID] = &runningInstance{inst: inst, fingerprint: fp, report: report}
	m.mu.Unlock()
}

// expansionVars is everything a placeholder may stand for: the directories,
// the operator's settings and — by name only — their secrets.
func expansionVars(p *config.PluginInstance, cwd, home string) map[string]string {
	vars := map[string]string{
		"__dirname": cwd, "HOME": home, "pathSeparator": string(filepath.Separator), "/": string(filepath.Separator),
	}
	for k, v := range p.Config {
		vars["user_config."+k] = fmt.Sprint(v)
	}
	for _, c := range p.Credentials {
		if c.Kind == config.CredentialEnv || c.Kind == config.CredentialSecret {
			vars["user_config."+c.Name] = c.Token
		}
	}
	return vars
}
