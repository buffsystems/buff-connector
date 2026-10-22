package admin

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/provider"
)

// asker prompts for one answer, falling back to def on an empty line or EOF.
type asker func(prompt, def string) string

// newAsker builds the prompt reader the wizard's sections share, so the terminal
// and the browser paths ask their questions the same way.
func newAsker(in io.Reader, out io.Writer) asker {
	sc := bufio.NewScanner(in)
	return func(prompt, def string) string {
		if def != "" {
			fmt.Fprintf(out, "%s [%s]: ", prompt, def)
		} else {
			fmt.Fprintf(out, "%s: ", prompt)
		}
		if !sc.Scan() {
			return def
		}
		if v := strings.TrimSpace(sc.Text()); v != "" {
			return v
		}
		return def
	}
}

// parseSelection turns a user's repo pick ("1,3,5", "all", or "" for none) into a
// set of 0-based indices within [0,n). Out-of-range/garbage tokens are ignored.
func parseSelection(input string, n int) []int {
	input = strings.TrimSpace(strings.ToLower(input))
	if input == "" {
		return nil
	}
	if input == "all" || input == "*" {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out
	}
	seen := map[int]bool{}
	var out []int
	for _, tok := range strings.FieldsFunc(input, func(r rune) bool { return r == ',' || r == ' ' }) {
		if v, err := strconv.Atoi(strings.TrimSpace(tok)); err == nil && v >= 1 && v <= n && !seen[v-1] {
			seen[v-1] = true
			out = append(out, v-1)
		}
	}
	return out
}

// RunWizard is the interactive CLI equivalent of the web setup UI — for headless
// hosts. Same guarantees: the token stays local (written to a 0600 file), repo
// lists come straight from the provider. Reuses config.Save + provider.ListRepos.
func RunWizard(ctx context.Context, cfgPath, version string, pair PairFunc, in io.Reader, out io.Writer) error {
	ask := newAsker(in, out)

	cur, _ := config.LoadLenient(cfgPath)
	fmt.Fprintln(out, "Настройка коннектора Buff. Токен остаётся у вас (в локальном файле).")
	mw := ask("Адрес relay (обычно менять не нужно)", firstNonEmpty(cur.MiddlewareURL, config.DefaultRelay))
	stateDir := ask("Каталог состояния (state_dir)", firstNonEmpty(cur.StateDir, "/var/lib/connector"))
	cfg := &config.Config{MiddlewareURL: mw, StateDir: stateDir}

	secretsDir := filepath.Join(stateDir, "secrets")
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		return err
	}
	for {
		kind := ask("Провайдер (gitlab/ghe/github/gitea/bitbucket-server/bitbucket, пусто=готово)", "")
		if kind == "" {
			break
		}
		base := ask("Base URL", "")
		token := ask("Токен доступа", "")
		p, err := provider.New(provider.Kind(kind), base, "_/_", nil)
		if err != nil {
			fmt.Fprintf(out, "  ошибка: %v\n", err)
			continue
		}
		lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		repos, err := p.ListRepos(lctx, token)
		cancel()
		if err != nil {
			fmt.Fprintf(out, "  нет доступа: %v\n", err)
			continue
		}
		if len(repos) == 0 {
			fmt.Fprintln(out, "  токен валиден, но репозиториев не видно — пропускаю")
			continue
		}
		for i, r := range repos {
			priv := ""
			if r.Private {
				priv = " (private)"
			}
			fmt.Fprintf(out, "   %d) %s%s\n", i+1, r.Repo, priv)
		}
		sel := parseSelection(ask("Выберите репозитории (напр. 1,3 или all)", ""), len(repos))
		if len(sel) == 0 {
			fmt.Fprintln(out, "  ничего не выбрано — пропускаю провайдера")
			continue
		}
		chosen := make([]string, 0, len(sel))
		for _, i := range sel {
			chosen = append(chosen, repos[i].Repo)
		}
		tokenFile := filepath.Join(secretsDir, fmt.Sprintf("provider-%d.token", len(cfg.Providers)))
		if err := os.WriteFile(tokenFile, []byte(token), 0o600); err != nil {
			return err
		}
		cfg.Providers = append(cfg.Providers, config.Provider{Kind: kind, BaseURL: base, Repos: chosen, TokenFile: tokenFile})
		fmt.Fprintf(out, "  добавлено: %s (%d репо)\n", kind, len(chosen))
	}

	// Integrations are no longer configured here: they come from the catalogue,
	// and the wizard's job ends at git. `connector catalog` and `connector
	// install` are the path, and the local page shows the same catalogue.
	if len(cfg.Providers) == 0 && len(cfg.Plugins) == 0 {
		fmt.Fprintln(out, "\nНи одного провайдера не добавлено. Это допустимо: интеграции")
		fmt.Fprintln(out, "ставятся отдельно — посмотрите `connector catalog`.")
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "Конфиг сохранён: %s\n", cfgPath)

	code := ask("Код привязки из приложения Buff (пусто=позже)", "")
	if code != "" {
		loaded, err := config.Load(cfgPath)
		if err != nil {
			return err
		}
		label, _ := os.Hostname()
		if err := pair(loaded, code, label, version); err != nil {
			fmt.Fprintf(out, "привязка не удалась: %v\n", err)
			return err
		}
		fmt.Fprintln(out, "готово — агент привязан. Запустите: connector run --config", cfgPath)
	} else {
		fmt.Fprintln(out, "Позже выполните: connector pair --config", cfgPath, "--code <код>")
	}
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
