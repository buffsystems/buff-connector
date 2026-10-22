package mcpsource

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Runner executes one command in dir and returns everything it printed. It is
// injected so the manager runs it under the connection's own user: installing
// a stranger's dependencies is the one step most likely to run a stranger's
// script, and it must not run as the connector.
type Runner func(ctx context.Context, dir string, env []string, name string, args ...string) (string, error)

// Toolchain is what this machine offers a custom server: absolute paths, or
// empty when the tool is absent — or present but unreachable by another user.
type Toolchain struct {
	Node   string `json:"node"`
	Npm    string `json:"npm"`
	Npx    string `json:"npx"`
	Python string `json:"python"`
	Uvx    string `json:"uvx"`
}

// FindToolchain resolves the tools against PATH, keeping only what the
// connection's user can execute.
func FindToolchain(lookPath func(string) (string, error), reachable func(string) bool) Toolchain {
	find := func(name string) string {
		p, err := lookPath(name)
		if err != nil || !reachable(p) {
			return ""
		}
		return p
	}
	tc := Toolchain{Node: find("node"), Npm: find("npm"), Npx: find("npx"), Uvx: find("uvx"), Python: find("python3")}
	if tc.Python == "" {
		tc.Python = find("python")
	}
	return tc
}

// Needs says whether the machine can run this launch, before anything is saved.
func (l Launch) Needs(tc Toolchain) error {
	switch l.Runtime {
	case "node":
		if tc.Node == "" {
			return errors.New("этому серверу нужен Node.js (18 или новее), а на этой машине его нет — или он " +
				"стоит там, куда пользователю подключения нет доступа (например, в /root). Поставьте " +
				"его пакетным менеджером системы")
		}
		if l.Deps == "npm" && tc.Npm == "" {
			return errors.New("для установки зависимостей нужен npm рядом с Node.js")
		}
		if l.Command == "npx" && tc.Npx == "" {
			return errors.New("этот сервер запускается через npx, а его на машине нет")
		}
	case "python":
		if tc.Python == "" && tc.Uvx == "" {
			return errors.New("этому серверу нужен Python 3 (или uv), а на этой машине его нет — или он " +
				"стоит там, куда пользователю подключения нет доступа")
		}
		if l.Command == "uvx" && tc.Uvx == "" {
			return errors.New("этот сервер запускается через uvx, а его на машине нет")
		}
	}
	return nil
}

// ResolveCommand turns the packaging's word for a runtime into the tool the
// machine has. Anything else is looked up as a command, placeholders included.
func ResolveCommand(command string, tc Toolchain, lookPath func(string) (string, error)) (string, error) {
	pick := func(p string) (string, error) {
		if p == "" {
			return "", fmt.Errorf("команда %q не найдена на этой машине (или недоступна пользователю подключения)", command)
		}
		return p, nil
	}
	switch command {
	case "node":
		return pick(tc.Node)
	case "npm":
		return pick(tc.Npm)
	case "npx":
		return pick(tc.Npx)
	case "python", "python3":
		return pick(tc.Python)
	case "uvx":
		return pick(tc.Uvx)
	}
	if strings.Contains(command, "/") || strings.Contains(command, "${") {
		return command, nil
	}
	p, err := lookPath(command)
	if err != nil {
		return "", fmt.Errorf("команда %q не найдена на этой машине: поставьте её так, чтобы коннектор "+
			"видел её в PATH, или укажите полный путь", command)
	}
	return p, nil
}

// Install fetches what the launch declares, in the code directory, and returns
// the tools' combined output — the whole diagnosis when something fails, and
// the record when nothing does.
func Install(ctx context.Context, dir string, l Launch, tc Toolchain, run Runner) (string, error) {
	var log strings.Builder
	step := func(name string, args ...string) error {
		fmt.Fprintf(&log, "$ %s %s\n", filepath.Base(name), strings.Join(args, " "))
		out, err := run(ctx, dir, nil, name, args...)
		log.WriteString(out)
		if out != "" && !strings.HasSuffix(out, "\n") {
			log.WriteByte('\n')
		}
		if err != nil {
			return fmt.Errorf("%s %s: %w", filepath.Base(name), strings.Join(args, " "), err)
		}
		return nil
	}
	switch l.Deps {
	case "npm":
		verb := "install"
		if _, err := os.Stat(filepath.Join(dir, "package-lock.json")); err == nil {
			verb = "ci"
		}
		if err := step(tc.Npm, verb, "--no-audit", "--no-fund"); err != nil {
			return log.String(), err
		}
		if l.Build {
			if err := step(tc.Npm, "run", "build"); err != nil {
				return log.String(), err
			}
		}
	case "pip":
		if err := step(tc.Python, "-m", "venv", ".venv"); err != nil {
			return log.String(), err
		}
		venvPython := filepath.Join(dir, ".venv", "bin", "python")
		if err := step(venvPython, "-m", "pip", "install", "--disable-pip-version-check", "."); err != nil {
			return log.String(), err
		}
	}
	return log.String(), nil
}
