package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/buffsystems/buff-connector/internal/problem"
)

// The store holds installed plugin code, addressed by the digest of the signed
// bundle it came from.
//
// Addressing by digest rather than by version is what lets two instances of the
// same plugin sit on different versions — one cautiously left behind while the
// other moves on — without the path ever encoding a version. The version lives
// in the manifest, where it belongs; the path names the artifact the signature
// covered.
//
// There is no index file. The store reads the manifests it has and builds the
// name→version map in memory at startup, so there is nothing that can drift out
// of step with what is actually on disk.

// Store is the plugins/ subtree of the connector's state directory.
type Store struct {
	root string
	keys []string
	rev  *Revocation
}

// Installed is one unpacked bundle.
type Installed struct {
	Digest   string
	Dir      string
	Manifest *Manifest
	// English is the bundle's EnglishFile, nil when it carries none.
	English *English
}

func NewStore(stateDir string, keys []string, rev *Revocation) *Store {
	return &Store{root: filepath.Join(stateDir, "plugins"), keys: keys, rev: rev}
}

// Install verifies, unpacks and records a bundle. Installing the same digest
// twice is a no-op rather than an error: an operator retrying after a network
// failure should not have to clean up first.
func (s *Store) Install(bundle, sig []byte) (*Installed, error) {
	if err := Verify(bundle, sig, s.keys); err != nil {
		return nil, err
	}
	digest := Digest(bundle)
	dir := filepath.Join(s.root, digest)

	if existing, err := s.open(digest); err == nil {
		return existing, nil
	}

	// Unpack beside the final location and move it into place in one step, so a
	// bundle interrupted half way cannot be mistaken for an installed one.
	tmp := dir + ".unpacking"
	if err := os.RemoveAll(tmp); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil, err
	}
	if err := unpack(bundle, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	man, err := readManifest(tmp)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	if err := s.checkRevocation(man); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.RemoveAll(tmp)
		// Another install of the same digest won the race — take its result.
		if existing, e := s.open(digest); e == nil {
			return existing, nil
		}
		return nil, err
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return nil, err
	}
	return &Installed{Digest: digest, Dir: dir, Manifest: man, English: readEnglish(dir)}, nil
}

// Resolve finds an installed plugin by name and version.
func (s *Store) Resolve(name, version string) (*Installed, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	for _, in := range all {
		if in.Manifest.Name == name && in.Manifest.Version == version {
			return in, nil
		}
	}
	return nil, problem.New("plugin_not_installed", name, version)
}

// List reads what is on disk. Directories that do not parse are skipped rather
// than fatal: one bad install must not hide every good one.
func (s *Store) List() ([]*Installed, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Installed
	for _, e := range entries {
		if !e.IsDir() || strings.Contains(e.Name(), ".") {
			continue
		}
		in, err := s.open(e.Name())
		if err != nil {
			continue
		}
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Manifest.Name != out[j].Manifest.Name {
			return out[i].Manifest.Name < out[j].Manifest.Name
		}
		return out[i].Manifest.Version < out[j].Manifest.Version
	})
	return out, nil
}

func (s *Store) open(digest string) (*Installed, error) {
	dir := filepath.Join(s.root, digest)
	man, err := readManifest(dir)
	if err != nil {
		return nil, err
	}
	return &Installed{Digest: digest, Dir: dir, Manifest: man, English: readEnglish(dir)}, nil
}

// CheckIntegrity re-hashes the unpacked tree and compares it with what was
// recorded at install time.
//
// Verifying only at install would be a false promise: the files sit in the
// customer's filesystem afterwards, and "readable means modifiable" was the
// argument we used against shipping the connector itself as source. It runs on
// every instance start — plugins are small and pure Python, so it costs nothing.
func (s *Store) CheckIntegrity(in *Installed) error {
	recorded, err := os.ReadFile(filepath.Join(in.Dir, treeSumFile))
	if err != nil {
		return problem.Wrap("plugin_integrity_missing", err, in.Manifest.Name)
	}
	actual, err := treeSum(in.Dir)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(recorded)) != actual {
		return problem.New("plugin_modified", in.Manifest.Name, in.Manifest.Version)
	}
	return nil
}

// Record writes the integrity record. Install does this once the tree is final.
func (s *Store) Record(in *Installed) error {
	sum, err := treeSum(in.Dir)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(in.Dir, treeSumFile), []byte(sum), 0o644)
}

func (s *Store) checkRevocation(m *Manifest) error {
	if s.rev == nil {
		return nil
	}
	key := m.Name + "@" + m.Version
	if reason, blocked := s.rev.Blocked[key]; blocked {
		return fmt.Errorf("%w: %s (%s)", ErrRevoked, key, reason)
	}
	return nil
}

// IsCritical reports whether a withdrawn version must stop a RUNNING instance,
// rather than only warn.
//
// The default is to warn and keep running. A withdrawal check happens on every
// start, so making it fatal by default would turn any restart — a machine
// rebooting at 4am — into a silently dead integration. Only a withdrawal we mark
// critical is worth that.
func (s *Store) IsCritical(m *Manifest) bool {
	return s.rev != nil && s.rev.Critical[m.Name+"@"+m.Version]
}

// Blocked reports the reason a version is withdrawn, if it is.
func (s *Store) Blocked(m *Manifest) (string, bool) {
	if s.rev == nil {
		return "", false
	}
	r, ok := s.rev.Blocked[m.Name+"@"+m.Version]
	return r, ok
}

const treeSumFile = ".buff-tree-sha256"

func readManifest(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	if err != nil {
		return nil, fmt.Errorf("в пакете нет manifest.yaml: %w", err)
	}
	return ParseManifest(b)
}

// treeSum hashes every file's path, mode and content, so a change anywhere in
// the tree changes the sum.
func treeSum(dir string) (string, error) {
	h := sha256.New()
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == treeSumFile {
			return nil
		}
		if info.IsDir() {
			fmt.Fprintf(h, "d:%s\n", rel)
			return nil
		}
		fmt.Fprintf(h, "f:%s:%o:", rel, info.Mode().Perm())
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		fmt.Fprintln(h)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Root is the directory the store keeps bundles in. Exported for tests that
// stage an installed plugin without building a signed bundle first.
func (s *Store) Root() string { return s.root }
