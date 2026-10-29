package devgate

// The project's repositories for Buff Code (#372), served as their own MCP server at
// /v1/repos/mcp. Buff Code registers it as «repos», so the model calls repos_glob, repos_grep
// and repos_read — named apart from its own glob/grep/read, which see only the working
// directory. They are the same opencode tools «Спросить» runs: the backend runs them over the
// project's checkout, so their input here is opencode's own, plus a branch and a page cursor.
// Read only; the backend decides the scope (the work context's project in the X-Buff-* headers).

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/buffsystems/buff-connector/internal/i18n"
)

type reposSurface struct{ link Requester }

const (
	branchProp = `"branch":{"type":"string"},`
	cursorProp = `"cursor":{"type":"integer"}`
)

// The words are in tools/repos.<lang>.md.
var reposTools = mustToolSet("repos", []toolSpec{
	{name: "glob", readOnly: true, schema: `{"type":"object","properties":{` +
		`"pattern":{"type":"string"},"path":{"type":"string"},"hidden":{"type":"boolean"},"limit":{"type":"integer"},` +
		branchProp + cursorProp +
		`},"required":["pattern","path","hidden","limit","branch","cursor"],"additionalProperties":false}`},
	{name: "grep", readOnly: true, schema: `{"type":"object","properties":{` +
		`"pattern":{"type":"string"},"path":{"type":"string"},"include":{"type":"string"},"literal":{"type":"boolean"},` +
		`"caseSensitive":{"type":"boolean"},"limit":{"type":"integer"},` +
		branchProp + cursorProp +
		`},"required":["pattern","path","include","literal","caseSensitive","limit","branch","cursor"],"additionalProperties":false}`},
	{name: "read", readOnly: true, schema: `{"type":"object","properties":{` +
		`"path":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"},` +
		branchProp + cursorProp +
		`},"required":["path","offset","limit","branch","cursor"],"additionalProperties":false}`},
})

// The fields each tool relays — nothing else the model sends reaches the backend.
var reposFields = map[string]struct {
	op     string
	fields []string
}{
	"glob": {"developer_repo_glob", []string{"pattern", "path", "hidden", "limit", "branch", "cursor"}},
	"grep": {"developer_repo_grep", []string{"pattern", "path", "include", "literal", "caseSensitive", "limit", "branch", "cursor"}},
	"read": {"developer_repo_read", []string{"path", "offset", "limit", "branch", "cursor"}},
}

func (r reposSurface) catalog(l i18n.Lang) []Tool { return reposTools.in(l) }

func (r reposSurface) instructions(ctx context.Context, c caller) string {
	reply, err := relay(ctx, r.link, "developer_repos", c, nil)
	if err != nil {
		return c.lang.Sprintf("Репозитории проекта сейчас недоступны: %v", err)
	}
	text, _ := reply["instructions"].(string)
	return text
}

func (r reposSurface) call(ctx context.Context, name string, args json.RawMessage, c caller) (json.RawMessage, error) {
	tool, ok := reposFields[name]
	if !ok {
		return nil, fmt.Errorf("инструмента %q нет", name)
	}
	var in map[string]any
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("%s: аргументы не разобраны: %v", name, err)
	}
	extra := map[string]any{}
	for _, f := range tool.fields {
		if v, ok := in[f]; ok {
			extra[f] = v
		}
	}
	reply, err := relay(ctx, r.link, tool.op, c, extra)
	if err != nil {
		return nil, err
	}
	return pageResult(c.lang, reply)
}

// The tool's own text as the model would see it locally; a long answer says where it goes on.
func pageResult(l i18n.Lang, reply map[string]any) (json.RawMessage, error) {
	text, _ := reply["output"].(string)
	if next, ok := reply["next"].(float64); ok {
		text += l.Sprintf("\n\n[Выдача не закончилась: вызови этот инструмент ещё раз с теми же аргументами и cursor=%d]", int64(next))
	}
	return json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}})
}
