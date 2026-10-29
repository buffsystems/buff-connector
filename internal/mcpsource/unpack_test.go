package mcpsource

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buffsystems/buff-connector/internal/i18n"
)

type entry struct {
	name    string
	body    string
	mode    int64
	link    string
	symlink bool
}

func zipOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		mode := os.FileMode(e.mode)
		if e.mode == 0 {
			mode = 0o644
		}
		if strings.HasSuffix(e.name, "/") {
			mode |= os.ModeDir
		}
		if e.symlink {
			mode |= os.ModeSymlink
		}
		h.SetMode(mode)
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		body := e.body
		if e.symlink {
			body = e.link
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tgzOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	w := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.mode == 0 {
			h.Mode = 0o644
		}
		if strings.HasSuffix(e.name, "/") {
			h.Typeflag, h.Size = tar.TypeDir, 0
		}
		if e.symlink {
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, e.link, 0
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := w.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return string(b)
}

// Every format the page accepts lands as the same tree: an .mcpb is a zip, a
// GitHub download is a zip with one folder on top, a tarball is what a Python
// project ships as. The folder on top is stripped — the operator dropped a
// server, not a folder named after a commit.
func TestUnpackAcceptsEveryFormatAndStripsTheTopFolder(t *testing.T) {
	cases := map[string][]byte{
		"weather.mcpb":   zipOf(t, entry{name: "manifest.json", body: "{}"}, entry{name: "server/index.js", body: "js"}),
		"weather.dxt":    zipOf(t, entry{name: "manifest.json", body: "{}"}, entry{name: "server/index.js", body: "js"}),
		"repo-main.zip":  zipOf(t, entry{name: "repo-main/"}, entry{name: "repo-main/manifest.json", body: "{}"}, entry{name: "repo-main/server/index.js", body: "js"}),
		"weather.tar.gz": tgzOf(t, entry{name: "weather-0.3/manifest.json", body: "{}"}, entry{name: "weather-0.3/server/index.js", body: "js"}),
		"weather.tgz":    tgzOf(t, entry{name: "./manifest.json", body: "{}"}, entry{name: "./server/index.js", body: "js"}),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			dest := t.TempDir()
			if err := Unpack(name, data, dest); err != nil {
				t.Fatalf("unpack: %v", err)
			}
			if read(t, filepath.Join(dest, "manifest.json")) != "{}" || read(t, filepath.Join(dest, "server", "index.js")) != "js" {
				t.Fatalf("tree is not the server's own: %v", ls(t, dest))
			}
		})
	}
}

// The format is decided by what the bytes ARE when the name lies or says
// nothing — a download saved as "archive" is still an archive.
func TestUnpackSniffsTheFormatWhenTheNameSaysNothing(t *testing.T) {
	dest := t.TempDir()
	if err := Unpack("download", tgzOf(t, entry{name: "a.txt", body: "a"}), dest); err != nil {
		t.Fatalf("tgz by content: %v", err)
	}
	dest2 := t.TempDir()
	if err := Unpack("download", zipOf(t, entry{name: "a.txt", body: "a"}), dest2); err != nil {
		t.Fatalf("zip by content: %v", err)
	}
	if err := Unpack("notes.txt", []byte("hello"), t.TempDir()); err == nil || !strings.Contains(i18n.LocalizeError(i18n.EN, err), "archive") {
		t.Fatalf("a non-archive must be refused with the reason: %v", err)
	}
}

// An archive is a stranger's file list turned into paths. Anything that could
// leave the directory is refused outright, not sanitised.
func TestUnpackRefusesEscapesAndLinks(t *testing.T) {
	bad := map[string][]byte{
		"zip-slip":     zipOf(t, entry{name: "../evil.txt", body: "x"}),
		"absolute":     tgzOf(t, entry{name: "/etc/passwd", body: "x"}),
		"symlink-zip":  zipOf(t, entry{name: "link", symlink: true, link: "/etc/passwd"}),
		"symlink-tar":  tgzOf(t, entry{name: "link", symlink: true, link: "../../etc"}),
		"dotdot-inner": tgzOf(t, entry{name: "a/../../evil", body: "x"}),
	}
	for name, data := range bad {
		t.Run(name, func(t *testing.T) {
			dest := t.TempDir()
			if err := Unpack("x.zip", data, dest); err == nil {
				t.Fatalf("accepted: %v", ls(t, dest))
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "evil.txt")); err == nil {
				t.Fatal("the escape was written before the refusal")
			}
		})
	}
}

// Executable bits survive: a binary server or a shebang script must stay runnable.
func TestUnpackKeepsExecutableBits(t *testing.T) {
	dest := t.TempDir()
	if err := Unpack("s.tgz", tgzOf(t, entry{name: "run.sh", body: "#!/bin/sh\n", mode: 0o755}), dest); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dest, "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Fatal("executable bit lost")
	}
}

// macOS resource forks are noise, not the server, and must not stop the top
// folder from being recognised as the only real one.
func TestUnpackIgnoresMacOSNoise(t *testing.T) {
	dest := t.TempDir()
	data := zipOf(t, entry{name: "__MACOSX/"}, entry{name: "__MACOSX/._x", body: "junk"},
		entry{name: "srv/"}, entry{name: "srv/manifest.json", body: "{}"})
	if err := Unpack("x.zip", data, dest); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dest, "manifest.json")) != "{}" {
		t.Fatalf("top folder not stripped: %v", ls(t, dest))
	}
}

func ls(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(p string, _ os.FileInfo, _ error) error {
		out = append(out, strings.TrimPrefix(p, dir))
		return nil
	})
	return out
}
