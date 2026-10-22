package plugin

import (
	"archive/tar"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// A bundle is the unit that crosses into the customer's perimeter: plugin
// source, its pinned pure-Python wheels, the manifest, and our signature over
// the whole thing.
//
// Format is tar+gzip rather than the zstd the design sketched, for one reason:
// gzip is in the standard library. The connector's two direct dependencies are
// part of the argument that it is cheap to audit, and a compression format is
// not worth spending the third on.
//
// Unpacking is the dangerous step — it is the moment we take a stranger's file
// list and turn it into paths. Everything a tar can do to escape its directory
// is refused here rather than sanitised, because a bundle doing any of it is not
// a bundle we should be running at all.

// ErrUntrusted means the bundle is not signed by a key this build trusts.
var ErrUntrusted = errors.New("bundle is not signed by a trusted key")

// ErrRevoked means the bundle is signed, but we have withdrawn it.
var ErrRevoked = errors.New("bundle version has been withdrawn")

// TrustedKeys are the publisher keys this build accepts. It is a SET, not one
// key: instances stay pinned to old versions for a long time, so a new key has
// to be trusted alongside the old one long before the old one goes away.
//
// Baked in at build time:
//
//	-ldflags "-X gitlab.com/.../internal/plugin.trustedKeysCSV=<key>,<key>"
//
// and overridable through the environment for a self-hosted catalogue or for the
// project's own end-to-end stand.
var TrustedKeys = loadTrustedKeys()

var trustedKeysCSV = ""

func loadTrustedKeys() []string {
	raw := trustedKeysCSV
	if v := os.Getenv("BUFF_TRUSTED_KEYS"); v != "" {
		raw = v
	}
	var out []string
	for _, k := range strings.Split(raw, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// MaxBundleSize bounds both the archive and what it expands to, so a bundle
// cannot fill the customer's disk.
const MaxBundleSize = 256 << 20

// Revocation is the signed list of versions that must not run. It travels with
// the offline kit as well as the catalogue, because otherwise an install from a
// file would simply route around it.
type Revocation struct {
	Serial   int64             `json:"serial"`
	Blocked  map[string]string `json:"blocked"`  // "name@version" -> reason
	Critical map[string]bool   `json:"critical"` // stop a running instance rather than warn
}

// Verify checks the detached signature against every trusted key.
func Verify(bundle, sig []byte, keys []string) error {
	if len(keys) == 0 {
		return fmt.Errorf("%w: this build trusts no publisher keys", ErrUntrusted)
	}
	for _, k := range keys {
		raw, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			continue
		}
		if ed25519.Verify(ed25519.PublicKey(raw), bundle, sig) {
			return nil
		}
	}
	return ErrUntrusted
}

// Digest identifies a bundle by content. It is what the unpacked directory is
// named after, so the path itself is the thing the signature covered.
func Digest(bundle []byte) string {
	sum := sha256.Sum256(bundle)
	return hex.EncodeToString(sum[:])
}

// unpack expands the archive into dir, refusing everything a tar can do to reach
// outside it.
func unpack(archive []byte, dir string) error {
	if len(archive) > MaxBundleSize {
		return fmt.Errorf("bundle is larger than the %d byte limit", MaxBundleSize)
	}
	gz, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return fmt.Errorf("bundle is not a gzip archive: %w", err)
	}
	defer gz.Close()

	var written int64
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading bundle: %w", err)
		}
		target, err := safeJoin(dir, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			written += h.Size
			if written > MaxBundleSize {
				return fmt.Errorf("bundle expands past the %d byte limit", MaxBundleSize)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			// Copy one byte past the declared size so a lying header is caught
			// rather than trusted.
			n, err := io.Copy(f, io.LimitReader(tr, h.Size+1))
			f.Close()
			if err != nil {
				return err
			}
			if n != h.Size {
				return fmt.Errorf("bundle entry %s does not match its declared size", h.Name)
			}
		case tar.TypeSymlink, tar.TypeLink:
			// A link is how an archive reaches a file it does not contain — the
			// identity seed, /etc/shadow, another instance's state. There is no
			// legitimate use for one inside a plugin bundle.
			return fmt.Errorf("bundle entry %s is a link, which is not allowed", h.Name)
		default:
			return fmt.Errorf("bundle entry %s has an unsupported type", h.Name)
		}
	}
}

// safeJoin resolves an archive path inside dir, refusing anything that climbs
// out — the classic zip-slip.
func safeJoin(dir, name string) (string, error) {
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("bundle entry %s is an absolute path", name)
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("bundle entry %s climbs out of the bundle", name)
	}
	target := filepath.Join(dir, clean)
	if !strings.HasPrefix(target, filepath.Clean(dir)+string(filepath.Separator)) {
		return "", fmt.Errorf("bundle entry %s resolves outside the bundle", name)
	}
	return target, nil
}
