package plugin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buffsystems/buff-connector/internal/i18n"
	"github.com/buffsystems/buff-connector/internal/problem"
)

// buildBundle makes a signed bundle the way the publisher will.
func buildBundle(t *testing.T, files map[string]string) (bundle, sig []byte, pub string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()

	pk, sk, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	bundle = buf.Bytes()
	return bundle, ed25519.Sign(sk, bundle), base64.StdEncoding.EncodeToString(pk)
}

const miniManifest = `
name: demo
version: 1.0.0
host_api: 1
entry: main.py
`

func TestInstallVerifiesUnpacksAndResolves(t *testing.T) {
	bundle, sig, pub := buildBundle(t, map[string]string{
		"manifest.yaml": miniManifest,
		"main.py":       "print('hi')",
	})
	root := t.TempDir()
	s := NewStore(root, []string{pub}, nil)

	in, err := s.Install(bundle, sig)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if in.Manifest.Name != "demo" {
		t.Fatalf("manifest = %+v", in.Manifest)
	}
	if !strings.HasSuffix(in.Dir, in.Digest) {
		t.Errorf("the directory must be named after the digest the signature covered: %s", in.Dir)
	}
	if _, err := os.Stat(filepath.Join(in.Dir, "main.py")); err != nil {
		t.Fatalf("code not unpacked: %v", err)
	}
	got, err := s.Resolve("demo", "1.0.0")
	if err != nil || got.Digest != in.Digest {
		t.Fatalf("resolve = %+v, %v", got, err)
	}
}

func TestInstallIsIdempotentForTheSameDigest(t *testing.T) {
	bundle, sig, pub := buildBundle(t, map[string]string{"manifest.yaml": miniManifest, "main.py": "x"})
	s := NewStore(t.TempDir(), []string{pub}, nil)

	a, err := s.Install(bundle, sig)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	b, err := s.Install(bundle, sig)
	if err != nil {
		t.Fatalf("second install of the same bundle must not fail: %v", err)
	}
	if a.Digest != b.Digest {
		t.Fatal("same bundle produced two digests")
	}
}

func TestUnsignedOrWrongKeyIsRefused(t *testing.T) {
	bundle, sig, _ := buildBundle(t, map[string]string{"manifest.yaml": miniManifest})
	_, otherPub := mustKey(t)
	s := NewStore(t.TempDir(), []string{otherPub}, nil)

	if _, err := s.Install(bundle, sig); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("err = %v, want ErrUntrusted", err)
	}
	if _, err := s.Install(bundle, []byte("garbage")); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("err = %v, want ErrUntrusted", err)
	}
}

// A key SET, not a key: an instance pinned to an old version must keep working
// while we rotate the publishing key.
func TestAnyTrustedKeyInTheSetIsAccepted(t *testing.T) {
	bundle, sig, pub := buildBundle(t, map[string]string{"manifest.yaml": miniManifest})
	_, retired := mustKey(t)
	s := NewStore(t.TempDir(), []string{retired, pub}, nil)

	if _, err := s.Install(bundle, sig); err != nil {
		t.Fatalf("install: %v", err)
	}
}

func TestWithdrawnVersionIsRefusedAtInstall(t *testing.T) {
	bundle, sig, pub := buildBundle(t, map[string]string{"manifest.yaml": miniManifest})
	s := NewStore(t.TempDir(), []string{pub}, &Revocation{
		Serial:  7,
		Blocked: map[string]string{"demo@1.0.0": "credential leak"},
	})
	_, err := s.Install(bundle, sig)
	if !errors.Is(err, ErrRevoked) || !strings.Contains(err.Error(), "credential leak") {
		t.Fatalf("err = %v, want a refusal carrying the reason", err)
	}
}

// A withdrawal warns by default and only stops a running instance when we mark
// it critical: an integrity check runs on every start, so a fatal default would
// turn a 4am reboot into a silently dead integration.
func TestWithdrawalIsOnlyFatalWhenMarkedCritical(t *testing.T) {
	m, _ := ParseManifest([]byte(miniManifest))
	warn := NewStore(t.TempDir(), nil, &Revocation{Blocked: map[string]string{"demo@1.0.0": "minor"}})
	if warn.IsCritical(m) {
		t.Error("an ordinary withdrawal must not stop a running instance")
	}
	if reason, ok := warn.Blocked(m); !ok || reason != "minor" {
		t.Errorf("the operator still has to be told: %q %v", reason, ok)
	}
	crit := NewStore(t.TempDir(), nil, &Revocation{
		Blocked:  map[string]string{"demo@1.0.0": "rce"},
		Critical: map[string]bool{"demo@1.0.0": true},
	})
	if !crit.IsCritical(m) {
		t.Error("a critical withdrawal must stop the instance")
	}
}

// Everything a tar can do to reach outside its directory.
func TestMaliciousArchivesAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(tw *tar.Writer)
		want  string
	}{
		{"path climbing out", func(tw *tar.Writer) {
			_ = tw.WriteHeader(&tar.Header{Name: "../escape.py", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
			_, _ = tw.Write([]byte("x"))
		}, "climbs out"},
		{"absolute path", func(tw *tar.Writer) {
			_ = tw.WriteHeader(&tar.Header{Name: "/etc/cron.d/x", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
			_, _ = tw.Write([]byte("x"))
		}, "absolute"},
		{"symlink to the identity seed", func(tw *tar.Writer) {
			_ = tw.WriteHeader(&tar.Header{
				Name: "link", Linkname: "../../identity/state.json", Typeflag: tar.TypeSymlink, Mode: 0o777,
			})
		}, "link"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			tc.build(tw)
			tw.Close()
			gz.Close()

			dir := t.TempDir()
			err := unpack(buf.Bytes(), dir)
			if err == nil || !strings.Contains(i18n.LocalizeError(i18n.EN, err), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// Verifying only at install would be a false promise: the files sit in the
// customer's filesystem afterwards, and readable means modifiable.
func TestIntegrityIsRecheckedAfterInstall(t *testing.T) {
	bundle, sig, pub := buildBundle(t, map[string]string{"manifest.yaml": miniManifest, "main.py": "print(1)"})
	s := NewStore(t.TempDir(), []string{pub}, nil)
	in, err := s.Install(bundle, sig)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := s.Record(in); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := s.CheckIntegrity(in); err != nil {
		t.Fatalf("freshly installed bundle fails its own check: %v", err)
	}

	if err := os.WriteFile(filepath.Join(in.Dir, "main.py"), []byte("print(2)  # tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckIntegrity(in); err == nil || !errors.Is(err, problem.New("plugin_modified")) {
		t.Fatalf("err = %v, want the tamper to be caught", err)
	}
}

func TestHalfUnpackedBundleIsNotMistakenForInstalled(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root, nil, nil)
	// What an interrupted install leaves behind.
	half := filepath.Join(root, "plugins", "deadbeef.unpacking")
	if err := os.MkdirAll(half, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(half, "manifest.yaml"), []byte(miniManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("an interrupted install was listed as installed: %+v", list)
	}
}

func mustKey(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pk, sk, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return sk, base64.StdEncoding.EncodeToString(pk)
}
