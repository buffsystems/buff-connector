package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/buffsystems/buff-connector/internal/i18n"
)

// fakeBilingual is a plugin built on the SDK that words its tools per language: it
// answers a listing in the language its _meta names (Russian when none), and says
// back which language a call came in.
func fakeBilingual(out *bufio.Writer) {
	reply := func(id json.Number, result any) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		out.Write(append(b, '\n'))
		out.Flush()
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var req struct {
			ID     json.Number `json:"id"`
			Method string      `json:"method"`
			Params struct {
				Meta map[string]string `json:"_meta"`
			} `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil {
			continue
		}
		lang := req.Params.Meta[LangMeta]
		switch req.Method {
		case "initialize":
			reply(req.ID, map[string]any{"protocolVersion": "2026-01-01", "hostApi": 1,
				"capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fake"}})
		case "tools/list":
			desc, param := "Найти задачи.", "что искать"
			if lang == "en" {
				desc, param = "Find issues.", "what to search for"
			}
			reply(req.ID, map[string]any{"tools": []any{map[string]any{
				"name": "search", "description": desc,
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
					"query": map[string]any{"type": "string", "description": param}}},
				"annotations": map[string]any{"readOnlyHint": true},
			}}})
		case "tools/call":
			if lang == "" {
				lang = "none"
			}
			reply(req.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": "lang=" + lang}}})
		default:
			reply(req.ID, map[string]any{})
		}
	}
}

func TestToolsAreWordedInTheLanguageAsked(t *testing.T) {
	in := newInstance(t, "bilingual", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := in.Start(ctx); err != nil {
		t.Fatal(err)
	}
	en, ru := in.ToolsIn(i18n.EN), in.ToolsIn(i18n.RU)
	if len(en) != 1 || en[0].Description != "Find issues." || !strings.Contains(string(en[0].InputSchema), "what to search for") {
		t.Errorf("English: %+v", en)
	}
	if len(ru) != 1 || ru[0].Description != "Найти задачи." || !strings.Contains(string(ru[0].InputSchema), "что искать") {
		t.Errorf("Russian: %+v", ru)
	}
	// What a tool IS does not depend on the language it is described in.
	if !en[0].ReadOnly || !ru[0].ReadOnly {
		t.Error("read-only in both languages")
	}
}

func TestACallCarriesItsLanguageToOurPlugin(t *testing.T) {
	in := newInstance(t, "bilingual", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := in.Start(ctx); err != nil {
		t.Fatal(err)
	}
	call := func(ctx context.Context) string {
		var out json.RawMessage
		if err := in.CallTool(ctx, "search", map[string]any{}, &out); err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if got := call(i18n.WithLang(ctx, i18n.EN)); !strings.Contains(got, "lang=en") {
		t.Errorf("English call: %s", got)
	}
	if got := call(i18n.WithLang(ctx, i18n.RU)); !strings.Contains(got, "lang=ru") {
		t.Errorf("Russian call: %s", got)
	}
	// A call that names no language leaves the plugin to its own (BUFF_LANG): nothing is sent.
	if got := call(ctx); !strings.Contains(got, "lang=none") {
		t.Errorf("unnamed call: %s", got)
	}
}

func TestAServerTheOperatorBroughtGetsNoLanguageOfOurs(t *testing.T) {
	in := NewInstance(&Starter{Priv: Privilege{CanSwitchUID: false}}, InstanceConfig{
		GUID: "GUIDTEST", Slug: "weather", Spec: fakeSpec("plain-mcp"), PlainMCP: true,
		StartBackoff: 10 * time.Millisecond, MaxBackoff: 40 * time.Millisecond, CallTimeout: 3 * time.Second,
	}, nil)
	t.Cleanup(in.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := in.Start(ctx); err != nil {
		t.Fatal(err)
	}
	var out json.RawMessage
	if err := in.CallTool(i18n.WithLang(ctx, i18n.EN), "forecast", map[string]any{}, &out); err != nil {
		t.Fatalf("the server was sent a field MCP does not define: %v", err)
	}
	if len(in.ToolsIn(i18n.EN)) != 3 {
		t.Errorf("its tools in any language are its own: %+v", in.ToolsIn(i18n.EN))
	}
}
