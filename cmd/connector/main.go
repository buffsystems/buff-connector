// Command connector is the Buff self-hosted git connector agent.
//
//	connector pair --config <path> --code <pairing-code> [--label <name>]
//	connector run  --config <path>
//	connector version
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/buffsystems/buff-connector/internal/admin"
	"github.com/buffsystems/buff-connector/internal/agent"
	"github.com/buffsystems/buff-connector/internal/catalog"
	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/i18n"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// catalogBase is where the plugin catalogue is published. Baked into the build
// like the relay address, so an operator never copies it by hand.
//
// It is a container REGISTRY rather than a host of our own. A customer with a
// closed perimeter already knows what to do with one — mirror it into their own
// Harbor or Nexus, allow it, scan it — and pointing this at that mirror is then
// the only change they make. It also means one less public host for us to run.
// The registry is transport: the catalogue and every bundle carry our signature,
// checked here against the keys baked into this build.
var catalogBase = "oci://registry.buff.systems/buff/plugins"

const defaultAdminAddr = "127.0.0.1:8765"

// version is stamped at build time (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	args, lang, set, err := takeLang(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.LocalizeError(i18n.Default(), err))
		os.Exit(2)
	}
	if set {
		i18n.SetDefault(lang)
	}
	os.Args = append(os.Args[:1], args...)
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "pair":
		cmdPair(os.Args[2:])
	case "run":
		cmdRun(os.Args[2:])
	case "catalog":
		cmdCatalog(os.Args[2:])
	case "install":
		cmdInstall(os.Args[2:])
	case "instance":
		cmdInstance(os.Args[2:])
	case "setup":
		cmdSetup(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, i18n.T("использование:"))
	fmt.Fprintln(os.Stderr, i18n.T("  connector setup --config <путь> [--addr 127.0.0.1:8765]   # локальная страница: провайдеры, токены, репозитории, привязка"))
	fmt.Fprintln(os.Stderr, i18n.T("  connector catalog [show <плагин>] --config <путь>        # что можно поставить"))
	fmt.Fprintln(os.Stderr, i18n.T("  connector install <плагин>[@версия] --config <путь>     # поставить из каталога"))
	fmt.Fprintln(os.Stderr, i18n.T("  connector instance list|show|set|secret|rm|check|log     # подключения, для скриптов"))
	fmt.Fprintln(os.Stderr, i18n.T("  connector run   --config <путь> [--admin 127.0.0.1:8765]  # работа (и, по желанию, локальная страница)"))
	fmt.Fprintln(os.Stderr, i18n.T("  connector pair  --config <путь> --code <код-привязки> [--label <имя>]"))
	fmt.Fprintln(os.Stderr, i18n.T("  connector version — версия"))
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, i18n.T("  --lang en|ru  язык вывода (по умолчанию — BUFF_LANG, затем LANGUAGE/LC_ALL/LC_MESSAGES/LANG, иначе русский)"))
}

// takeLang takes the --lang flag out of the command line wherever it stands,
// before the command parses its own flags and refuses it as unknown. The
// language it names outranks BUFF_LANG and the locale.
func takeLang(args []string) (rest []string, lang i18n.Lang, set bool, err error) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "lang" {
			rest = append(rest, a)
			continue
		}
		if !hasValue {
			if i+1 >= len(args) {
				return nil, "", false, fmt.Errorf("--lang ждёт значение — en или ru")
			}
			i++
			value = args[i]
		}
		l, ok := i18n.Parse(value)
		if !ok {
			return nil, "", false, fmt.Errorf("--lang %q — нужно en или ru", value)
		}
		lang, set = l, true
	}
	return rest, lang, set, nil
}

// cmdSetup serves ONLY the local web UI (127.0.0.1) — for first-time configuration
// and pairing, before/without a running agent. Nothing leaves the machine.
func cmdSetup(args []string) {
	fs := i18n.Flags("setup")
	cfgPath := fs.String("config", "", i18n.T("путь к YAML-конфигу (создаётся или обновляется здесь)"))
	addr := fs.String("addr", defaultAdminAddr, i18n.T("локальный адрес страницы"))
	cli := fs.Bool("cli", false, i18n.T("мастер в терминале вместо страницы (для машин без браузера)"))
	i18n.ParseFlags(fs, args)
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, i18n.T("setup требует --config"))
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if *cli {
		if err := admin.RunWizard(ctx, *cfgPath, version, agent.Pair, os.Stdin, os.Stdout); err != nil {
			i18n.Fatalf("настройка: %v", err)
		}
		return
	}
	// The URL carries a one-time token: the local surface installs plugin code and
	// writes secrets, so loopback alone is not a credential.
	srv := admin.New(*cfgPath, version, agent.Pair)
	i18n.Logf("настройка коннектора — откройте %s в браузере", srv.URL(*addr))
	if err := srv.Serve(ctx, *addr); err != nil {
		i18n.Fatalf("настройка: %v", err)
	}
}

// catalogClient points at the published catalogue. The address is baked into the
// build like the relay is, and overridable for a self-hosted deployment or for
// the project's own tests.
func catalogClient(cfgPath string) *catalog.Client {
	base := catalogBase
	if v := os.Getenv("BUFF_CATALOG_URL"); v != "" {
		base = v
	}
	cache := ""
	if cfg, err := config.LoadLenient(cfgPath); err == nil && cfg.StateDir != "" {
		cache = filepath.Join(cfg.StateDir, "catalog")
	}
	return catalog.New(base, plugin.TrustedKeys, cache)
}

func cmdPair(args []string) {
	fs := i18n.Flags("pair")
	cfgPath := fs.String("config", "", i18n.T("путь к YAML-конфигу"))
	code := fs.String("code", "", i18n.T("одноразовый код привязки из приложения Buff"))
	label := fs.String("label", "", i18n.T("человеческое имя коннектора (по умолчанию — имя машины)"))
	i18n.ParseFlags(fs, args)
	if *cfgPath == "" || *code == "" {
		fmt.Fprintln(os.Stderr, i18n.T("pair требует --config и --code"))
		os.Exit(2)
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		i18n.Fatalf("конфиг: %v", err)
	}
	lbl := *label
	if lbl == "" {
		if h, err := os.Hostname(); err == nil {
			lbl = h
		}
	}
	if err := agent.Pair(cfg, *code, lbl, version); err != nil {
		i18n.Fatalf("привязка: %v", err)
	}
	fmt.Println(i18n.T("привязано — теперь можно запустить: connector run --config"), *cfgPath)
}

func cmdRun(args []string) {
	fs := i18n.Flags("run")
	cfgPath := fs.String("config", "", i18n.T("путь к YAML-конфигу"))
	adminAddr := fs.String("admin", "", i18n.T("заодно открыть локальную страницу на этом адресе (например, 127.0.0.1:8765)"))
	i18n.ParseFlags(fs, args)
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, i18n.T("run требует --config"))
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// Optionally serve the local setup UI alongside the agent — edits are applied
	// live (the run loop watches the config file), so no restart is needed.
	if *adminAddr != "" {
		go func() {
			srv := admin.New(*cfgPath, version, agent.Pair)
			// Wait for the agent to bring the plugin manager up, then wire the
			// plugin surface: without it the page still configures git, which is
			// what the connector does out of the box.
			for i := 0; i < 50 && (agent.Plugins == nil || agent.MCP == nil); i++ {
				time.Sleep(100 * time.Millisecond)
			}
			if agent.Plugins != nil {
				srv = srv.WithPlugins(agent.Plugins, catalogClient(*cfgPath))
			}
			if agent.MCP != nil {
				srv = srv.WithIDE(agent.MCP)
			}
			i18n.Logf("локальная страница — откройте %s", srv.URL(*adminAddr))
			if err := srv.Serve(ctx, *adminAddr); err != nil {
				i18n.Logf("локальная страница: %v", err)
			}
		}()
	}
	if err := agent.Run(ctx, *cfgPath, version); err != nil && err != context.Canceled {
		i18n.Fatalf("работа: %v", err)
	}
	log.Println(i18n.T("коннектор остановлен"))
}
