// Package transport chunks a bundle for upload over the command plane. NATS caps
// a message near 1MB, and base64 inflates by 4/3, so raw chunks stay well under
// that. Streaming (never loading the whole 2GB bundle into memory) is the point.
package transport

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"os"
)

// DefaultChunkSize keeps a base64 chunk (~4/3 larger) comfortably under the NATS
// 1MB message limit.
const DefaultChunkSize = 512 * 1024

// Chunk is one piece of a streamed blob.
type Chunk struct {
	Seq  int    `json:"seq"`
	Data string `json:"data"` // base64(std) of the raw bytes
	Last bool   `json:"last"`
}

// ChunkFile streams `path` to `fn` in base64 chunks of `size` raw bytes. The
// final chunk is flagged Last (a one-byte lookahead detects end-of-file even when
// the size divides evenly). An empty file yields exactly one terminal chunk.
func ChunkFile(path string, size int, fn func(Chunk) error) error {
	if size <= 0 {
		size = DefaultChunkSize
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("открытие файла: %w", err)
	}
	defer f.Close()

	r := bufio.NewReaderSize(f, size)
	buf := make([]byte, size)
	seq := 0
	sentAny := false
	for {
		n, rerr := io.ReadFull(r, buf)
		if rerr != nil && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
			return fmt.Errorf("чтение файла: %w", rerr)
		}
		if n > 0 {
			// Peek one byte to learn whether anything follows this chunk.
			_, perr := r.Peek(1)
			last := perr == io.EOF
			if err := fn(Chunk{Seq: seq, Data: base64.StdEncoding.EncodeToString(buf[:n]), Last: last}); err != nil {
				return err
			}
			seq++
			sentAny = true
			if last {
				return nil
			}
			continue
		}
		// n == 0 → EOF reached with nothing buffered.
		break
	}
	if !sentAny {
		return fn(Chunk{Seq: 0, Data: "", Last: true})
	}
	return nil
}
