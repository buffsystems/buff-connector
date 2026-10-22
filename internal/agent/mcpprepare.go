package agent

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/mcpsource"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// Preparing a custom server (#351) is everything that happens between "the
// operator dropped a file" and "the operator sees a form": the code lands in
// a directory owned by the connection's user, the packaging is read, and the
// dependencies are installed — by that user, never by the connector. What
// comes back is a draft: the launch as the packaging describes it, the log of
// what the tools printed, and the reason if the machine cannot run it.

// PrepareCustomArchive unpacks an archive the operator dropped and installs
// what it declares.
func (m *PluginManager) PrepareCustomArchive(ctx context.Context, name string, data []byte) (mcpsource.Draft, error) {
	return m.prepare(ctx, config.MCPFromArchive, name, func(dir string) error {
		return mcpsource.Unpack(name, data, dir)
	})
}

// PrepareCustomGitHub downloads a repository and does the same.
func (m *PluginManager) PrepareCustomGitHub(ctx context.Context, url, token string) (mcpsource.Draft, error) {
	repo, err := mcpsource.ParseGitHub(url)
	if err != nil {
		return mcpsource.Draft{}, err
	}
	return m.prepare(ctx, config.MCPFromGitHub, url, func(dir string) error {
		data, err := mcpsource.FetchGitHub(ctx, http.DefaultClient, m.githubAPI(), repo, token)
		if err != nil {
			return err
		}
		return mcpsource.Unpack(repo.Name+".zip", data, dir)
	})
}

func (m *PluginManager) githubAPI() string {
	if m.github != "" {
		return m.github
	}
	return mcpsource.GitHubAPI
}

func (m *PluginManager) prepare(ctx context.Context, source, origin string, fetch func(dir string) error) (mcpsource.Draft, error) {
	draft := mcpsource.Draft{ID: config.NewInstanceID(), Source: source, Origin: origin, Toolchain: m.Toolchain()}
	uid, err := m.uids.For(draft.ID)
	if err != nil {
		return draft, err
	}
	dir, err := plugin.PrepareCodeDir(m.stateDir, draft.ID, uid, uid)
	if err != nil {
		return draft, err
	}
	if err := fetch(dir); err != nil {
		return draft, err
	}
	if err := plugin.ChownTree(dir, uid, uid); err != nil {
		return draft, err
	}
	if draft.Launch, err = mcpsource.Detect(dir); err != nil {
		return draft, err
	}
	if err := draft.Launch.Needs(draft.Toolchain); err != nil {
		draft.Needs = err.Error()
		return draft, nil
	}
	if draft.Launch.Deps == "" {
		return draft, nil
	}
	if !m.starter.Priv.CanSwitchUID {
		draft.Needs = "зависимости не установлены: " + m.starter.Priv.Reason
		return draft, nil
	}
	home, err := plugin.PrepareInstanceDir(m.stateDir, draft.ID, uid, uid)
	if err != nil {
		return draft, err
	}
	draft.Log, err = mcpsource.Install(ctx, dir, draft.Launch, draft.Toolchain, m.runner(uid, home))
	if err != nil {
		draft.Needs = "зависимости не установились: " + err.Error()
	}
	return draft, nil
}

// runner executes a tool as the connection's user, in the code directory,
// with the connection's own home — and returns everything it printed.
func (m *PluginManager) runner(uid uint32, home string) mcpsource.Runner {
	return func(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
		spec := plugin.Spec{
			Path: name, Args: args, Dir: dir, UID: uid, GID: uid,
			Env: append([]string{
				"HOME=" + home, "TMPDIR=" + home, "PATH=" + os.Getenv("PATH"),
				"npm_config_cache=" + filepath.Join(home, ".npm"),
				"CI=1",
			}, env...),
		}
		proc, err := m.starter.Start(ctx, spec)
		if err != nil {
			return "", err
		}
		var out bytes.Buffer
		var mu sync.Mutex
		var wg sync.WaitGroup
		drain := func(r io.Reader) {
			defer wg.Done()
			buf := make([]byte, 4096)
			for {
				n, err := r.Read(buf)
				if n > 0 {
					mu.Lock()
					out.Write(buf[:n])
					mu.Unlock()
				}
				if err != nil {
					return
				}
			}
		}
		wg.Add(2)
		go drain(proc.Stdout)
		go drain(proc.Stderr)
		_ = proc.Stdin.Close()
		wg.Wait()
		err = proc.Wait()
		return out.String(), err
	}
}

// CodeFields re-reads what the packaging asks the operator, for the settings
// form of a saved custom server; nothing when the code is not on disk (a typed
// command, a remote server).
func (m *PluginManager) CodeFields(id string) []mcpsource.Field {
	l, err := mcpsource.Detect(CodeDir(m.stateDir, id))
	if err != nil {
		return nil
	}
	return l.Fields
}
