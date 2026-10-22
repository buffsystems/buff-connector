package agent

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
)

// The fake custom server is this test binary re-executed with an env marker: a
// minimal server speaking the published MCP protocol, the way one from npm or
// PyPI would — and nothing of buff/*.
func TestMain(m *testing.M) {
	if os.Getenv("BUFF_FAKE_MCP") != "" {
		fakeMCPMain()
		return
	}
	os.Exit(m.Run())
}

func fakeMCPMain() {
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	send := func(msg map[string]any) {
		b, _ := json.Marshal(msg)
		out.Write(append(b, '\n'))
		out.Flush()
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		var msg struct {
			ID     *json.Number    `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil || msg.ID == nil || msg.Method == "" {
			continue
		}
		switch msg.Method {
		case "initialize":
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "weather", "version": "0.3.0"},
			}})
		case "tools/list":
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"tools": []any{
				map[string]any{"name": "lookup", "description": "Look a city up.",
					"inputSchema": map[string]any{"type": "object"},
					"annotations": map[string]any{"readOnlyHint": true}},
				map[string]any{"name": "set_home", "description": "Remember the home city.",
					"inputSchema": map[string]any{"type": "object"}},
			}}})
		case "tools/call":
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "Perm: " + os.Getenv("WEATHER_UNITS") + " " + os.Getenv("WEATHER_API_KEY")}},
			}})
		default:
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32601, "message": "unknown " + msg.Method}})
		}
	}
}
