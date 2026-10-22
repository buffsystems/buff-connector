package natslink

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func msg(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestAPlainReplyIsWhole(t *testing.T) {
	var a assembler
	ev, err := a.feed([]byte(`{"ok":true,"x":1}`))
	if err != nil || ev.kind != evWhole || string(ev.body) != `{"ok":true,"x":1}` {
		t.Fatalf("%+v %v", ev, err)
	}
}

// The backend splits a reply that does not fit one message (#337); the connector used to
// read only the first chunk and fail on it.
func TestSizeChunksAreAssembledInAnyOrder(t *testing.T) {
	var a assembler
	body := `{"ok":true,"text":"довольно длинный ответ"}`
	parts := []string{body[:10], body[10:25], body[25:]}
	order := []int{2, 0, 1}
	var ev event
	for i, n := range order {
		var err error
		ev, err = a.feed(msg(t, map[string]any{"chunk": map[string]any{"id": "c1", "seq": n, "of": 3}, "data": b64(parts[n])}))
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 && ev.kind != evNone {
			t.Fatalf("early event %+v", ev)
		}
	}
	if ev.kind != evWhole || string(ev.body) != body {
		t.Fatalf("assembled %+v", ev)
	}
}

func TestAStreamGivesPiecesBeatsAndAnEnd(t *testing.T) {
	var a assembler
	want := []eventKind{evPiece, evBeat, evPiece, evEnd}
	frames := []any{
		map[string]any{"chunk": map[string]any{"id": "s1", "seq": 0}, "data": b64("При")},
		map[string]any{"chunk": map[string]any{"id": "s1", "beat": true}},
		map[string]any{"chunk": map[string]any{"id": "s1", "seq": 1}, "data": b64("вет")},
		map[string]any{"chunk": map[string]any{"id": "s1", "seq": 2, "last": true}},
	}
	text := ""
	for i, f := range frames {
		ev, err := a.feed(msg(t, f))
		if err != nil {
			t.Fatal(err)
		}
		if ev.kind != want[i] {
			t.Fatalf("frame %d: kind %v, want %v", i, ev.kind, want[i])
		}
		text += string(ev.body)
	}
	if text != "Привет" || a.streamID != "s1" || a.lastSeq != 1 {
		t.Fatalf("text %q id %q last %d", text, a.streamID, a.lastSeq)
	}
}

func TestAStreamErrorIsReportedInItsOwnWords(t *testing.T) {
	var a assembler
	ev, err := a.feed(msg(t, map[string]any{"chunk": map[string]any{"id": "s1", "seq": 0, "error": "провайдер не ответил"}}))
	if err != nil || ev.kind != evFail || ev.err != "провайдер не ответил" {
		t.Fatalf("%+v %v", ev, err)
	}
}

// #404: a frame lost on the way — the connection was replaced when its token expired — is
// asked for again, and the stream goes on from it; a frame sent twice is taken once.
func TestAGapAsksForTheMissingFrameAndTheStreamGoesOn(t *testing.T) {
	var a assembler
	feed := func(seq int, data string) event {
		t.Helper()
		ev, err := a.feed(msg(t, map[string]any{"chunk": map[string]any{"id": "s1", "seq": seq}, "data": b64(data)}))
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}
	if ev := feed(0, "a"); ev.kind != evPiece {
		t.Fatalf("%+v", ev)
	}
	if ev := feed(2, "c"); ev.kind != evGap || a.nextSeq != 1 {
		t.Fatalf("a gap must ask for frame 1: %+v next %d", ev, a.nextSeq)
	}
	if ev := feed(1, "b"); ev.kind != evPiece || string(ev.body) != "b" {
		t.Fatalf("%+v", ev)
	}
	if ev := feed(2, "c"); ev.kind != evPiece || string(ev.body) != "c" {
		t.Fatalf("the resent frame continues the stream: %+v", ev)
	}
	if ev := feed(1, "b"); ev.kind != evNone {
		t.Fatalf("a frame taken already is skipped: %+v", ev)
	}
}

func TestTheEndIsRememberedToBeConfirmed(t *testing.T) {
	var a assembler
	if _, err := a.feed(msg(t, map[string]any{"chunk": map[string]any{"id": "s1", "seq": 0}, "data": b64("a")})); err != nil {
		t.Fatal(err)
	}
	ev, err := a.feed(msg(t, map[string]any{"chunk": map[string]any{"id": "s1", "seq": 1, "last": true}}))
	if err != nil || ev.kind != evEnd || a.endSeq != 1 {
		t.Fatalf("%+v %v end %d", ev, err, a.endSeq)
	}
}
