// Command connector is the Buff self-hosted git connector agent.
//
//	connector pair --config <path> --code <pairing-code> [--label <name>]
//	connector run  --config <path>
//	connector version
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/buffsystems/buff-connector/internal/admin"
	"github.com/buffsystems/buff-connector/internal/agent"
	"github.com/buffsystems/buff-connector/internal/catalog"
	"github.com/buffsystems/buff-connector/internal/config"
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
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  connector setup --config <path> [--addr 127.0.0.1:8765]   # local web UI: providers, tokens, repos, pairing")
	fmt.Fprintln(os.Stderr, "  connector catalog [show <plugin>] --config <path>        # what can be installed")
	fmt.Fprintln(os.Stderr, "  connector install <plugin>[@version] --config <path>     # install from the catalogue")
	fmt.Fprintln(os.Stderr, "  connector instance list|show|set|secret|rm|check|log     # connections, scriptable")
	fmt.Fprintln(os.Stderr, "  connector run   --config <path> [--admin 127.0.0.1:8765]  # run (optionally serving the local web UI)")
	fmt.Fprintln(os.Stderr, "  connector pair  --config <path> --code <pairing-code> [--label <name>]")
	fmt.Fprintln(os.Stderr, "  connector version")
}

// cmdSetup serves ONLY the local web UI (127.0.0.1) — for first-time configuration
// and pairing, before/without a running agent. Nothing leaves the machine.
func cmdSetup(args []string) {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to the YAML config file (created/updated here)")
	addr := fs.String("addr", defaultAdminAddr, "loopback address for the web UI")
	cli := fs.Bool("cli", false, "interactive terminal wizard instead of the web UI (for headless hosts)")
	_ = fs.Parse(args)
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "setup requires --config")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if *cli {
		if err := admin.RunWizard(ctx, *cfgPath, version, agent.Pair, os.Stdin, os.Stdout); err != nil {
			log.Fatalf("setup: %v", err)
		}
		return
	}
	// The URL carries a one-time token: the local surface installs plugin code and
	// writes secrets, so loopback alone is not a credential.
	srv := admin.New(*cfgPath, version, agent.Pair)
	log.Printf("connector setup — open %s in your browser", srv.URL(*addr))
	if err := srv.Serve(ctx, *addr); err != nil {
		log.Fatalf("setup: %v", err)
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
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to the YAML config file")
	code := fs.String("code", "", "one-time pairing code from the Buff app")
	label := fs.String("label", "", "human label for this connector (default: hostname)")
	_ = fs.Parse(args)
	if *cfgPath == "" || *code == "" {
		fmt.Fprintln(os.Stderr, "pair requires --config and --code")
		os.Exit(2)
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	lbl := *label
	if lbl == "" {
		if h, err := os.Hostname(); err == nil {
			lbl = h
		}
	}
	if err := agent.Pair(cfg, *code, lbl, version); err != nil {
		log.Fatalf("pair: %v", err)
	}
	fmt.Println("paired — you can now run: connector run --config", *cfgPath)
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to the YAML config file")
	adminAddr := fs.String("admin", "", "also serve the local web UI on this loopback address (e.g. 127.0.0.1:8765)")
	_ = fs.Parse(args)
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "run requires --config")
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
			log.Printf("local web UI — open %s", srv.URL(*adminAddr))
			if err := srv.Serve(ctx, *adminAddr); err != nil {
				log.Printf("admin server: %v", err)
			}
		}()
	}
	if err := agent.Run(ctx, *cfgPath, version); err != nil && err != context.Canceled {
		log.Fatalf("run: %v", err)
	}
	log.Println("connector stopped")
}
