// Package mcpsource turns what the operator brought — an archive, a repository
// — into a directory the connector can run a custom MCP server from (#351).
//
// Nobody signed any of it. What the package can still promise is that the
// bytes land where they are meant to and nowhere else, that the server is
// started the way its own packaging says, and that the operator sees the
// command before anything runs.
package mcpsource

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrNotArchive means the bytes are not a format the page accepts.
var ErrNotArchive = errors.New("this is not an archive the connector can unpack (.mcpb, .dxt, .zip, .tar.gz, .tgz)")

// An item is one member of any archive, read into memory before anything is
// written. Two passes are the point: the top folder can only be stripped once
// every name is known, and a refusal must happen before the first byte lands.
type item struct {
	name string
	dir  bool
	mode os.FileMode
	body []byte
}

// Unpack writes the archive into dest, which must exist and be empty.
func Unpack(name string, data []byte, dest string) error {
	items, err := list(name, data)
	if err != nil {
		return err
	}
	items = stripTopFolder(items)
	for _, it := range items {
		target, err := safeJoin(dest, it.name)
		if err != nil {
			return err
		}
		if it.dir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, it.body, it.mode|0o600); err != nil {
			return err
		}
	}
	return nil
}

// list reads every member, refusing anything that is not a plain file or a
// directory: a link is a way out of the directory, whatever it points at today.
func list(name string, data []byte) ([]item, error) {
	switch {
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return listZip(data)
	case bytes.HasPrefix(data, []byte{0x1f, 0x8b}):
		return listTar(data, true)
	case strings.HasSuffix(name, ".tar"):
		return listTar(data, false)
	}
	return nil, ErrNotArchive
}

func listZip(data []byte) ([]item, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotArchive, err)
	}
	var out []item
	for _, f := range zr.File {
		if noise(f.Name) {
			continue
		}
		mode := f.Mode()
		if mode&os.ModeSymlink != 0 || mode&os.ModeType&^os.ModeDir != 0 {
			return nil, fmt.Errorf("archive member %q is a link or a device, refused", f.Name)
		}
		if err := checkName(f.Name); err != nil {
			return nil, err
		}
		it := item{name: f.Name, dir: mode.IsDir() || strings.HasSuffix(f.Name, "/"), mode: mode.Perm()}
		if !it.dir {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			it.body, err = io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, err
			}
		}
		out = append(out, it)
	}
	return out, nil
}

func listTar(data []byte, gzipped bool) ([]item, error) {
	var r io.Reader = bytes.NewReader(data)
	if gzipped {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNotArchive, err)
		}
		defer gz.Close()
		r = gz
	}
	tr := tar.NewReader(r)
	var out []item
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNotArchive, err)
		}
		if noise(h.Name) {
			continue
		}
		switch h.Typeflag {
		case tar.TypeReg, tar.TypeDir:
		default:
			return nil, fmt.Errorf("archive member %q is a link or a device, refused", h.Name)
		}
		if err := checkName(h.Name); err != nil {
			return nil, err
		}
		it := item{name: h.Name, dir: h.Typeflag == tar.TypeDir, mode: os.FileMode(h.Mode).Perm()}
		if !it.dir {
			it.body, err = io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, it)
	}
	return out, nil
}

// noise is what archivers add and no server needs.
func noise(name string) bool {
	clean := strings.TrimPrefix(name, "./")
	return strings.HasPrefix(clean, "__MACOSX/") || clean == "__MACOSX" || path.Base(clean) == ".DS_Store" ||
		clean == "" || clean == "."
}

// checkName refuses a member name that is absolute or climbs out.
func checkName(name string) error {
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) || filepath.IsAbs(name) {
		return fmt.Errorf("archive member %q has an absolute path, refused", name)
	}
	for _, part := range strings.Split(strings.ReplaceAll(name, `\`, "/"), "/") {
		if part == ".." {
			return fmt.Errorf("archive member %q climbs out of the archive, refused", name)
		}
	}
	return nil
}

// safeJoin is the last line: even a name that passed checkName is joined and
// verified to sit under dest.
func safeJoin(dest, name string) (string, error) {
	target := filepath.Join(dest, filepath.FromSlash(name))
	rel, err := filepath.Rel(dest, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive member %q would land outside the target directory", name)
	}
	return target, nil
}

// stripTopFolder removes the single directory every member sits under, when
// there is one — a GitHub download, a tarball of a release.
func stripTopFolder(items []item) []item {
	top := ""
	for _, it := range items {
		clean := strings.TrimPrefix(it.name, "./")
		first, rest, found := strings.Cut(clean, "/")
		if !found || rest == "" {
			if it.dir && !found {
				first = clean
			} else if !it.dir {
				return items // a file at the root: nothing to strip
			}
		}
		if top == "" {
			top = first
		} else if first != top {
			return items
		}
	}
	if top == "" {
		return items
	}
	out := make([]item, 0, len(items))
	for _, it := range items {
		clean := strings.TrimPrefix(it.name, "./")
		rest := strings.TrimPrefix(strings.TrimPrefix(clean, top), "/")
		if rest == "" {
			continue
		}
		it.name = rest
		out = append(out, it)
	}
	return out
}
