package natslink

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Reading a command's answer off the inbox. Three shapes share it, byte-for-byte with the
// backend that answers commands and the web app:
//
//	plain:  any JSON without a "chunk" key — the whole answer in one message
//	sized:  {"chunk":{"id","seq","of"},"data":<base64>} — one answer split by size (#337)
//	stream: {"chunk":{"id","seq"},"data"} pieces, then {"last":true} or {"error":"…"};
//	        {"chunk":{"id","beat":true}} says «alive, nothing new» and takes no seq (#365)

type eventKind int

const (
	evNone  eventKind = iota // a sized chunk arrived, the answer is not complete yet
	evWhole                  // the whole answer (plain, or sized and assembled)
	evPiece                  // a piece of a stream
	evBeat                   // the stream is alive
	evEnd                    // the stream ended normally
	evFail                   // the stream ended with an error
	evGap                    // a frame is missing: ask the producer to resume from nextSeq (#404)
)

type event struct {
	kind eventKind
	body []byte
	err  string
}

type chunkHead struct {
	ID    string `json:"id"`
	Seq   *int   `json:"seq"`
	Of    *int   `json:"of"`
	Last  bool   `json:"last"`
	Error string `json:"error"`
	Beat  bool   `json:"beat"`
}

type frame struct {
	Chunk *chunkHead `json:"chunk"`
	Data  string     `json:"data"`
}

// assembler turns the messages of ONE answer into events. It is not safe for concurrent use.
type assembler struct {
	sized    map[int][]byte
	of       int
	streamID string
	nextSeq  int
	lastSeq  int // the seq of the last piece received — what the reader acknowledges
	endSeq   int // the seq of the closing frame — confirmed so the producer can let go
}

func (a *assembler) feed(raw []byte) (event, error) {
	var f frame
	if err := json.Unmarshal(raw, &f); err != nil {
		return event{}, fmt.Errorf("parse reply: %w", err)
	}
	if f.Chunk == nil {
		return event{kind: evWhole, body: raw}, nil
	}
	c := f.Chunk
	if c.Of != nil {
		return a.sizedChunk(c, f.Data)
	}
	if a.streamID == "" {
		a.streamID = c.ID
	} else if c.ID != a.streamID {
		return event{}, fmt.Errorf("кадр чужого потока %q в потоке %q", c.ID, a.streamID)
	}
	if c.Beat {
		return event{kind: evBeat}, nil
	}
	if c.Seq == nil {
		return event{}, fmt.Errorf("кадр потока без номера")
	}
	// Core NATS delivers at most once: a frame sent while the connection was being replaced
	// is lost (#404). A later one means a gap — the producer is asked to resume; one already
	// taken is a resent copy.
	if *c.Seq > a.nextSeq {
		return event{kind: evGap}, nil
	}
	if *c.Seq < a.nextSeq {
		return event{kind: evNone}, nil
	}
	a.nextSeq++
	switch {
	case c.Error != "":
		a.endSeq = *c.Seq
		return event{kind: evFail, err: c.Error}, nil
	case c.Last:
		a.endSeq = *c.Seq
		return event{kind: evEnd}, nil
	}
	data, err := base64.StdEncoding.DecodeString(f.Data)
	if err != nil {
		return event{}, fmt.Errorf("кадр потока: %w", err)
	}
	a.lastSeq = *c.Seq
	return event{kind: evPiece, body: data}, nil
}

func (a *assembler) sizedChunk(c *chunkHead, data string) (event, error) {
	if c.Seq == nil || *c.Of <= 0 || *c.Seq < 0 || *c.Seq >= *c.Of {
		return event{}, fmt.Errorf("неверный кусок ответа")
	}
	if a.sized == nil {
		a.sized, a.of = map[int][]byte{}, *c.Of
	}
	part, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return event{}, fmt.Errorf("кусок ответа: %w", err)
	}
	a.sized[*c.Seq] = part
	if len(a.sized) < a.of {
		return event{kind: evNone}, nil
	}
	var body []byte
	for i := 0; i < a.of; i++ {
		body = append(body, a.sized[i]...)
	}
	return event{kind: evWhole, body: body}, nil
}
