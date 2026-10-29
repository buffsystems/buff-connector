package devgate

import (
	"net/http"
	"strings"
	"testing"
)

// A picture for Buff Code (#27) goes to the backend as developer_draw with the work context,
// and comes back streamed as the backend wrote it.
func TestDrawRelaysThePromptAndStreamsThePicture(t *testing.T) {
	l := &streamLink{pieces: []string{
		`{"status":200,"content_type":"application/json"}` + "\n",
		`{"ok":true,"mime":"image/png","content":"iVBO`, `Rw0K","bytes":6}`,
	}}
	w := call(Handler(l), http.MethodPost, "/v1/draw", `{"prompt":"кот в шляпе"}`, dev)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"content":"iVBORw0K"`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	got := l.streamed[0].payload
	if got["op"] != "developer_draw" || got["token"] != "DEV" || got["prompt"] != "кот в шляпе" ||
		got["org"] != "acme" || got["project"] != "shop" || got["session"] != "ses_1" {
		t.Fatalf("%v", got)
	}
}

func TestDrawNeedsTheDevelopersToken(t *testing.T) {
	w := call(Handler(&streamLink{}), http.MethodPost, "/v1/draw", `{"prompt":"x"}`, map[string]string{})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("%d", w.Code)
	}
}
