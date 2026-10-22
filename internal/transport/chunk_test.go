package transport

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestChunkFileRoundTrip(t *testing.T) {
	// A payload that is not a multiple of the chunk size, to exercise the tail.
	data := bytes.Repeat([]byte("buff-connector-"), 1000) // 15_000 bytes
	dir := t.TempDir()
	p := filepath.Join(dir, "blob.bin")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}

	var got []byte
	seen := 0
	lastSeenLast := false
	err := ChunkFile(p, 4096, func(c Chunk) error {
		if c.Seq != seen {
			t.Fatalf("out-of-order seq %d, want %d", c.Seq, seen)
		}
		seen++
		lastSeenLast = c.Last
		raw, err := base64.StdEncoding.DecodeString(c.Data)
		if err != nil {
			t.Fatalf("bad base64: %v", err)
		}
		got = append(got, raw...)
		return nil
	})
	if err != nil {
		t.Fatalf("chunk: %v", err)
	}
	if !lastSeenLast {
		t.Fatal("final chunk was not flagged Last")
	}
	// ceil(15000/4096) = 4 chunks
	if seen != 4 {
		t.Fatalf("chunk count = %d, want 4", seen)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("reassembled bytes differ from source")
	}
}

func TestChunkFileExactMultiple(t *testing.T) {
	// Exactly 2× the chunk size — the last chunk must still be flagged Last.
	data := bytes.Repeat([]byte("x"), 8192)
	dir := t.TempDir()
	p := filepath.Join(dir, "blob.bin")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var lasts []bool
	var got []byte
	err := ChunkFile(p, 4096, func(c Chunk) error {
		lasts = append(lasts, c.Last)
		raw, _ := base64.StdEncoding.DecodeString(c.Data)
		got = append(got, raw...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lasts) != 2 || lasts[0] || !lasts[1] {
		t.Fatalf("expected 2 chunks with Last only on the 2nd, got %v", lasts)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("reassembled bytes differ")
	}
}

func TestChunkFileEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.bin")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	n := 0
	err := ChunkFile(p, 4096, func(c Chunk) error {
		n++
		if !c.Last || c.Data != "" || c.Seq != 0 {
			t.Fatalf("unexpected empty-file chunk: %+v", c)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chunk: %v", err)
	}
	if n != 1 {
		t.Fatalf("empty file should yield exactly one terminal chunk, got %d", n)
	}
}
