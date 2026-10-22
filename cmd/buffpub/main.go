// Command buffpub publishes the plugin catalogue.
//
// It is the other half of the trust story: it builds a bundle from a plugin
// directory, signs it, and writes an index that is ALSO signed. Signing bundles
// but not the index would leave the list forgeable, and a forged list is enough
// to talk a connector into installing a version we have since withdrawn.
//
//	buffpub key                                   # mint a publishing key
//	buffpub build --src <dir> --out <dir>         # bundle + signature
//	buffpub index --out <dir>                     # catalog.json + signature
//	buffpub push --dist <dir> --registry <host>   # publish to a container registry
//	buffpub kit --out <dir>                       # offline kit: the published files
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/buffsystems/buff-connector/internal/catalog"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: buffpub key|build|index|push|kit")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "key":
		keyCmd()
	case "build":
		buildCmd(os.Args[2:])
	case "index":
		indexCmd(os.Args[2:])
	case "push":
		pushCmd(os.Args[2:])
	case "kit":
		kitCmd(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}

func keyCmd() {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		die(err)
	}
	fmt.Printf("public:  %s\nprivate: %s\n",
		base64.StdEncoding.EncodeToString(pub),
		base64.StdEncoding.EncodeToString(priv))
}

func buildCmd(args []string) {
	flags := flag.NewFlagSet("build", flag.ExitOnError)
	src := flags.String("src", "", "plugin source directory (must hold manifest.yaml)")
	sdk := flags.String("sdk", "", "SDK directory to bundle alongside the plugin")
	out := flags.String("out", "dist", "output directory")
	_ = flags.Parse(args)

	manRaw, err := os.ReadFile(filepath.Join(*src, "manifest.yaml"))
	if err != nil {
		die(err)
	}
	man, err := plugin.ParseManifest(manRaw)
	if err != nil {
		die(err)
	}
	// Publishing is where the quality bar is enforced, because it is the only
	// place that sees every plugin: a tool with no description is a tool the
	// model will not know when to reach for.
	for _, t := range man.Tools {
		if strings.TrimSpace(t.Summary) == "" {
			die(fmt.Errorf("tool %s has no summary — the model needs to know when to use it", t.Name))
		}
	}

	bundle, err := tarGz(*src, *sdk)
	if err != nil {
		die(err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		die(err)
	}
	name := fmt.Sprintf("%s-%s.tar.gz", man.Name, man.Version)
	if err := os.WriteFile(filepath.Join(*out, name), bundle, 0o644); err != nil {
		die(err)
	}
	sig := ed25519.Sign(privateKey(), bundle)
	if err := os.WriteFile(filepath.Join(*out, name+".sig"), sig, 0o644); err != nil {
		die(err)
	}
	fmt.Printf("%s (%d bytes) signed\n", name, len(bundle))
}

func indexCmd(args []string) {
	flags := flag.NewFlagSet("index", flag.ExitOnError)
	out := flags.String("out", "dist", "directory holding the bundles")
	// The connector version this catalogue was published with. It travels here
	// because the catalogue is the one channel a connector already verifies, so
	// telling an operator their core is behind needs nothing new to trust (#302).
	// Left out, nothing is claimed — which is what an older publisher produced.
	conn := flags.String("connector", "", "newest connector version (e.g. v0.7.2)")
	_ = flags.Parse(args)

	entries := map[string]*catalog.Entry{}
	err := filepath.WalkDir(*out, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".tar.gz") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		man, err := manifestFromBundle(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		e, ok := entries[man.Name]
		if !ok {
			e = &catalog.Entry{Name: man.Name, Summary: man.Summary, Publisher: man.Publisher,
				Category: category(man)}
			entries[man.Name] = e
		}
		v := catalog.Version{
			Version: man.Version, HostAPI: man.HostAPI,
			Released: time.Now().UTC().Format("2006-01-02"),
			Size:     int64(len(raw)),
			Bundle:   filepath.Base(path), Signature: filepath.Base(path) + ".sig",
			Platforms: man.Platforms, Webhook: string(man.Webhook),
		}
		for _, t := range man.Tools {
			v.Tools = append(v.Tools, catalog.Tool{Name: t.Name, ReadOnly: t.ReadOnly, Vision: t.Vision, Summary: t.Summary})
		}
		for _, c := range man.Creds {
			v.Creds = append(v.Creds, c.Name)
			for _, k := range c.Kinds {
				if plugin.Handed(k) {
					v.Handed = append(v.Handed, c.Name)
					break
				}
			}
		}
		for _, g := range man.Egress {
			if g.Web != nil {
				v.Web = true
				continue
			}
			v.EgressFrom = append(v.EgressFrom, g.FromConfig)
		}
		for _, n := range man.Network {
			v.Network = append(v.Network, catalog.Network{Protocol: n.Protocol, HostsFrom: n.HostsFrom, Ports: n.Ports})
		}
		e.Versions = append(e.Versions, v)
		return nil
	})
	if err != nil {
		die(err)
	}

	idx := catalog.Index{GeneratedAt: time.Now().UTC(), Connector: *conn}
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		e := entries[n]
		sort.Slice(e.Versions, func(i, j int) bool { return e.Versions[i].Version > e.Versions[j].Version })
		idx.Plugins = append(idx.Plugins, *e)
	}

	body, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		die(err)
	}
	if err := os.WriteFile(filepath.Join(*out, "catalog.json"), body, 0o644); err != nil {
		die(err)
	}
	if err := os.WriteFile(filepath.Join(*out, "catalog.json.sig"), ed25519.Sign(privateKey(), body), 0o644); err != nil {
		die(err)
	}

	// An empty revocation list is still published and still signed: its absence
	// must not be indistinguishable from "nothing is withdrawn".
	rev, err := json.MarshalIndent(plugin.Revocation{Serial: time.Now().Unix(),
		Blocked: map[string]string{}, Critical: map[string]bool{}}, "", "  ")
	if err != nil {
		die(err)
	}
	_ = os.WriteFile(filepath.Join(*out, "revocations.json"), rev, 0o644)
	_ = os.WriteFile(filepath.Join(*out, "revocations.json.sig"), ed25519.Sign(privateKey(), rev), 0o644)

	fmt.Printf("catalog.json: %d plugins, signed\n", len(idx.Plugins))
}

func tarGz(src, sdk string) ([]byte, error) {
	var buf strings.Builder
	gz := gzip.NewWriter(&stringWriter{&buf})
	tw := tar.NewWriter(gz)

	add := func(root, prefix string) error {
		return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if strings.Contains(path, "__pycache__") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			name := filepath.ToSlash(filepath.Join(prefix, rel))
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644,
				Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
				return err
			}
			_, err = tw.Write(body)
			return err
		})
	}
	if err := add(src, ""); err != nil {
		return nil, err
	}
	if sdk != "" {
		if err := add(sdk, "buff_plugin"); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

type stringWriter struct{ b *strings.Builder }

func (w *stringWriter) Write(p []byte) (int, error) { return w.b.Write(p) }

func manifestFromBundle(raw []byte) (*plugin.Manifest, error) {
	gz, err := gzip.NewReader(strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, fmt.Errorf("no manifest.yaml in bundle")
		}
		if h.Name == "manifest.yaml" {
			body := make([]byte, h.Size)
			if _, err := tr.Read(body); err != nil && err.Error() != "EOF" {
				return nil, err
			}
			return plugin.ParseManifest(body)
		}
	}
}

func category(m *plugin.Manifest) string {
	if m.Webhook != plugin.WebhookNone || len(m.Creds) > 0 {
		return "интеграция"
	}
	return "инструменты"
}

func privateKey() ed25519.PrivateKey {
	raw := os.Getenv("BUFF_PUBLISH_KEY")
	if raw == "" {
		die(fmt.Errorf("set BUFF_PUBLISH_KEY to the base64 private key (buffpub key)"))
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(b) != ed25519.PrivateKeySize {
		die(fmt.Errorf("BUFF_PUBLISH_KEY is not an ed25519 private key"))
	}
	return ed25519.PrivateKey(b)
}

func die(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
