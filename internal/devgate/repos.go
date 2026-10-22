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
)

type reposSurface struct{ link Requester }

const (
	branchProp = `"branch":{"type":"string","description":"ветка проекта; пустая строка — основная"},`
	cursorProp = `"cursor":{"type":"integer","description":"0 — с начала; иначе число из прошлого ответа, где сказано продолжить"}`
)

var reposTools = []Tool{
	{
		Name: "glob",
		Description: "Найти файлы по маске в коде проекта в Buff. Вызывай, когда ищешь файл вне своего рабочего каталога.\n" +
			"Свой рабочий каталог ищи своим glob: этот видит только проект в Buff, пути — от его корня, модули — в " +
			"sources/<модуль>. Примеры маски: **/*.ts, sources/**/config*.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"pattern":{"type":"string","description":"маска файлов"},` +
			`"path":{"type":"string","description":"каталог поиска от корня проекта; пустая строка — весь проект"},` +
			`"hidden":{"type":"boolean","description":"true — со скрытыми файлами и каталогами"},` +
			`"limit":{"type":"integer","description":"сколько файлов вернуть; 0 — 100"},` +
			branchProp + cursorProp +
			`},"required":["pattern","path","hidden","limit","branch","cursor"],"additionalProperties":false}`),
		ReadOnly: true,
	},
	{
		Name: "grep",
		Description: "Найти строки по регулярному выражению в коде проекта в Buff. Вызывай до того, как гадать о соседнем коде.\n" +
			"Свой рабочий каталог ищи своим grep: этот видит только проект в Buff, пути — от его корня. Ищет и в тексте " +
			"документов Word, Excel, PowerPoint и PDF. Ответ — файлы с номерами строк.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"pattern":{"type":"string","description":"регулярное выражение или точный текст (см. literal)"},` +
			`"path":{"type":"string","description":"файл или каталог от корня проекта; пустая строка — весь проект"},` +
			`"include":{"type":"string","description":"маска файлов, например *.ts или *.{ts,tsx}; пустая строка — все"},` +
			`"literal":{"type":"boolean","description":"true — pattern как точный текст, не регулярное выражение"},` +
			`"caseSensitive":{"type":"boolean","description":"true — с учётом регистра (обычно)"},` +
			`"limit":{"type":"integer","description":"сколько строк-совпадений вернуть; 0 — 100"},` +
			branchProp + cursorProp +
			`},"required":["pattern","path","include","literal","caseSensitive","limit","branch","cursor"],"additionalProperties":false}`),
		ReadOnly: true,
	},
	{
		Name: "read",
		Description: "Прочитать файл или каталог из кода проекта в Buff. Вызывай, когда нашёл место через glob или grep.\n" +
			"Свои файлы читай своим read: этот видит только проект в Buff, путь — от его корня. Строки пронумерованы; " +
			"документы Word, Excel, PowerPoint и PDF читаются текстом. Большой файл читай частями через offset и limit.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"path":{"type":"string","description":"путь от корня проекта"},` +
			`"offset":{"type":"integer","description":"с какой строки (с 1); 0 — с начала"},` +
			`"limit":{"type":"integer","description":"сколько строк; 0 — 2000"},` +
			branchProp + cursorProp +
			`},"required":["path","offset","limit","branch","cursor"],"additionalProperties":false}`),
		ReadOnly: true,
	},
}

// The fields each tool relays — nothing else the model sends reaches the backend.
var reposFields = map[string]struct {
	op     string
	fields []string
}{
	"glob": {"developer_repo_glob", []string{"pattern", "path", "hidden", "limit", "branch", "cursor"}},
	"grep": {"developer_repo_grep", []string{"pattern", "path", "include", "literal", "caseSensitive", "limit", "branch", "cursor"}},
	"read": {"developer_repo_read", []string{"path", "offset", "limit", "branch", "cursor"}},
}

func (r reposSurface) catalog() []Tool { return reposTools }

func (r reposSurface) instructions(ctx context.Context, c caller) string {
	reply, err := relay(ctx, r.link, "developer_repos", c, nil)
	if err != nil {
		return "Репозитории проекта сейчас недоступны: " + err.Error()
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
	return pageResult(reply)
}

// The tool's own text as the model would see it locally; a long answer says where it goes on.
func pageResult(reply map[string]any) (json.RawMessage, error) {
	text, _ := reply["output"].(string)
	if next, ok := reply["next"].(float64); ok {
		text += fmt.Sprintf("\n\n[Выдача не закончилась: вызови этот инструмент ещё раз с теми же аргументами и cursor=%d]", int64(next))
	}
	return json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}})
}
