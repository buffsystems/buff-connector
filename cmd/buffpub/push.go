package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/buffsystems/buff-connector/internal/catalog"
	"github.com/buffsystems/buff-connector/internal/i18n"
	"github.com/buffsystems/buff-connector/internal/oci"
)

// push uploads a built catalogue to a registry.
//
// A registry rather than a static host because that is what a customer with a
// closed perimeter can work with: mirror it into their own, allow it, scan it.
// The files are byte for byte the ones `buffpub index` produced and signed — the
// registry is transport, and the connector still verifies our signature itself.
func pushCmd(args []string) {
	flags := i18n.Flags("push")
	dir := flags.String("dist", "dist", i18n.T("каталог, который собрали build и index"))
	registry := flags.String("registry", "", i18n.T("адрес реестра, например registry.buff.systems"))
	prefix := flags.String("prefix", "buff/plugins", i18n.T("префикс репозитория в реестре"))
	plain := flags.Bool("plain", false, i18n.T("http вместо https (реестр на локальном адресе)"))
	i18n.ParseFlags(flags, args)

	if *registry == "" {
		die(fmt.Errorf("нужен --registry"))
	}
	cl := oci.New(*registry)
	cl.Plain = *plain
	// Publishing needs an account; reading the catalogue never does.
	cl.User, cl.Pass = os.Getenv("BUFF_REGISTRY_USER"), os.Getenv("BUFF_REGISTRY_PASS")
	if cl.User == "" {
		die(fmt.Errorf("задайте BUFF_REGISTRY_USER и BUFF_REGISTRY_PASS"))
	}

	pushed, err := pushDist(context.Background(), cl, strings.Trim(*prefix, "/"), *dir)
	if err != nil {
		die(err)
	}
	for _, p := range pushed {
		fmt.Println(i18n.T("опубликовано"), p)
	}
}

// pushDist publishes every bundle the index names, then the revocation list,
// then the index itself.
//
// The order is the point. The catalogue is what a connector reads to decide what
// it may install, so publishing it before the bundles it names opens a window
// where an operator is offered an install that cannot succeed. Last means the
// catalogue only ever advertises what is already there.
func pushDist(ctx context.Context, cl *oci.Client, prefix, dir string) ([]string, error) {
	idxRaw, idxSig, err := readSigned(dir, "catalog.json")
	if err != nil {
		return nil, err
	}
	var idx catalog.Index
	if err := json.Unmarshal(idxRaw, &idx); err != nil {
		return nil, fmt.Errorf("catalog.json: %w", err)
	}

	repo := func(name string) string {
		if prefix == "" {
			return name
		}
		return prefix + "/" + name
	}

	var pushed []string
	for _, e := range idx.Plugins {
		for _, v := range e.Versions {
			body, sig, err := readSigned(dir, v.Bundle)
			if err != nil {
				return pushed, err
			}
			if err := cl.Push(ctx, repo(e.Name), v.Version, body, sig); err != nil {
				return pushed, err
			}
			pushed = append(pushed, repo(e.Name)+":"+v.Version)
		}
	}

	revRaw, revSig, err := readSigned(dir, "revocations.json")
	if err != nil {
		return pushed, err
	}
	if err := cl.Push(ctx, repo("revocations"), "v1", revRaw, revSig); err != nil {
		return pushed, err
	}
	pushed = append(pushed, repo("revocations")+":v1")

	if err := cl.Push(ctx, repo("catalog"), "v1", idxRaw, idxSig); err != nil {
		return pushed, err
	}
	return append(pushed, repo("catalog")+":v1"), nil
}

// readSigned reads a file and the detached signature beside it. A missing
// signature is a hard error: publishing an unsigned artifact would leave the
// connector to refuse it later, far away from whoever could fix it.
func readSigned(dir, name string) (body, sig []byte, err error) {
	if body, err = os.ReadFile(filepath.Join(dir, name)); err != nil {
		return nil, nil, err
	}
	if sig, err = os.ReadFile(filepath.Join(dir, name+".sig")); err != nil {
		return nil, nil, fmt.Errorf("%s не подписан: %w", name, err)
	}
	return body, sig, nil
}

// kitCmd writes an offline kit: the published catalogue and every bundle it
// names, as files.
//
// For a machine with no route anywhere. It is PULLED from the registry rather
// than rebuilt from source on purpose — a rebuild could differ from what a
// connected connector would install, and then two customers would be running
// different bytes under the same version number.
func kitCmd(args []string) {
	flags := i18n.Flags("kit")
	out := flags.String("out", "kit", i18n.T("каталог, куда записать набор"))
	registry := flags.String("registry", "registry.buff.systems", i18n.T("реестр, из которого брать"))
	prefix := flags.String("prefix", "buff/plugins", i18n.T("префикс репозитория в реестре"))
	plain := flags.Bool("plain", false, i18n.T("http вместо https"))
	i18n.ParseFlags(flags, args)

	cl := oci.New(*registry)
	cl.Plain = *plain
	files, err := pullKit(context.Background(), cl, strings.Trim(*prefix, "/"), *out)
	if err != nil {
		die(err)
	}
	fmt.Print(i18n.Sprintf("офлайн-комплект готов: %d файл(ов) в %s\n", len(files), *out))
	fmt.Println(i18n.T("на машине без сети: BUFF_CATALOG_URL=file://<путь> connector install <плагин> --config <файл>"))
}

// pullKit copies the published catalogue and its bundles into a directory.
func pullKit(ctx context.Context, cl *oci.Client, prefix, out string) ([]string, error) {
	repo := func(name string) string {
		if prefix == "" {
			return name
		}
		return prefix + "/" + name
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	var written []string
	save := func(name string, body, sig []byte) error {
		if err := os.WriteFile(filepath.Join(out, name), body, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, name+".sig"), sig, 0o644); err != nil {
			return err
		}
		written = append(written, name, name+".sig")
		return nil
	}

	idxRaw, idxSig, err := cl.Pull(ctx, repo("catalog"), "v1")
	if err != nil {
		return nil, err
	}
	var idx catalog.Index
	if err := json.Unmarshal(idxRaw, &idx); err != nil {
		return nil, fmt.Errorf("опубликованный каталог не читается: %w", err)
	}

	// Bundles first, catalogue last — the same order publishing uses, and for
	// the same reason: a kit whose index names a bundle it does not carry is a
	// kit that fails at install time, in front of the operator.
	for _, e := range idx.Plugins {
		for _, v := range e.Versions {
			body, sig, err := cl.Pull(ctx, repo(e.Name), v.Version)
			if err != nil {
				return written, fmt.Errorf("%s %s: %w", e.Name, v.Version, err)
			}
			if err := save(v.Bundle, body, sig); err != nil {
				return written, err
			}
		}
	}
	revRaw, revSig, err := cl.Pull(ctx, repo("revocations"), "v1")
	if err != nil {
		return written, err
	}
	// The withdrawal list travels with the kit: without it, installing from a
	// file would simply route around a version we have withdrawn.
	if err := save("revocations.json", revRaw, revSig); err != nil {
		return written, err
	}
	if err := save("catalog.json", idxRaw, idxSig); err != nil {
		return written, err
	}
	return written, nil
}
