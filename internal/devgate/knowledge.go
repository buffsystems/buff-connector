package devgate

// The organisation's knowledge for Buff Code (#371), served as its own MCP server at
// /v1/knowledge/mcp. Buff Code registers it as «knowledge», so the model calls the tools as
// knowledge_list_entries / knowledge_read_entry / knowledge_write_entry — the names a
// pipeline run uses. The backend decides the rest: the scope (the work context Buff Code
// sends in the X-Buff-* headers), what the developer may see, the text the model starts with.
// The connector adds the one thing the backend cannot: asking the person before an entry is
// added (owner's decision — the model never writes on its own from Buff Code).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type knowledgeSurface struct{ link Requester }

var strictName = json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"имя записи (латиницей, из индекса знаний)"}},"required":["name"],"additionalProperties":false}`)

// Code Mode (how Buff Code offers MCP tools) shows only the FIRST LINE of a description, cut at
// 120 characters — so that line carries what the tool does and the moment to call it; the rest
// of the contract follows on the next lines.
var knowledgeTools = []Tool{
	{
		Name: "list_entries",
		Description: "Список записей базы знаний этого контекста, без тел. Вызывай перед write_entry, чтобы не завести дубль.\n" +
			"Каждая запись: имя, заголовок, описание одной строкой, тип, область и автор. Запись с always_in_prompt уже " +
			"приведена целиком в твоём промпте.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`),
		ReadOnly:    true,
	},
	{
		Name: "read_entry",
		Description: "Прочитать запись базы знаний по имени из индекса. Вызывай, как только строка индекса отвечает на вопрос.\n" +
			"Делай это до того, как придумывать ответ или лезть в код. Возвращает {found, body, scope, author}; для записи, " +
			"которая уже приведена в промпте целиком, тело не повторяется (inlined: true).",
		InputSchema: strictName,
		ReadOnly:    true,
	},
	{
		Name: "write_entry",
		Description: "Добавить запись в базу знаний: вызов сам спрашивает пользователя формой. Не пиши «добавляю» — вызывай.\n" +
			"Вызывай В МОМЕНТ, когда пользователь просит что-то запомнить или формулирует правило, когда выяснил устойчивый " +
			"факт об окружении или проекте, или наткнулся на воспроизводимое препятствие и нашёл обход. Без «да» пользователя " +
			"ничего не записывается; written: false — он отказался, не предлагай эту запись снова. scope: org (вся " +
			"организация) или имя проекта либо пространства из рабочего контекста.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"name":{"type":"string","description":"имя-адрес записи латиницей через дефис"},` +
			`"title":{"type":"string","description":"заголовок для человека"},` +
			`"description":{"type":"string","description":"одна строка: о чём запись — по ней модель решает, читать ли тело"},` +
			`"type":{"type":"string","enum":["rule","fact","pitfall"],"description":"rule — правило работы; fact — как здесь устроено; pitfall — препятствие и обход"},` +
			`"scope":{"type":"string","description":"org или имя проекта/пространства из рабочего контекста"},` +
			`"body":{"type":"string","description":"тело в markdown: что именно нужно знать"}` +
			`},"required":["name","title","description","type","scope","body"],"additionalProperties":false}`),
	},
}

func (k knowledgeSurface) catalog() []Tool { return knowledgeTools }

// request relays one knowledge command with the developer's token and work context.
func (k knowledgeSurface) request(ctx context.Context, op string, c caller, extra map[string]any) (map[string]any, error) {
	return relay(ctx, k.link, op, c, extra)
}

// relay sends one developer command to the backend: the developer's token, the work context
// from the X-Buff-* headers Buff Code sends, and the command's own fields. The answer comes back
// without its ok flag; a refusal comes back as the backend worded it.
func relay(ctx context.Context, link Requester, op string, c caller, extra map[string]any) (map[string]any, error) {
	spaces := []string{}
	for _, s := range strings.Split(c.header.Get("X-Buff-Spaces"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			spaces = append(spaces, s)
		}
	}
	payload := map[string]any{"op": op, "token": c.token, "org": c.header.Get("X-Buff-Org"),
		"project": c.header.Get("X-Buff-Project"), "spaces": spaces}
	for key, v := range extra {
		payload[key] = v
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	reply, err := link.Request(ctx, "data.cmd."+op, payload)
	// ok:true is an answer even when it carries an `error` field — that is how «no such entry»
	// reads, as in a pipeline run's knowledge_read_entry; the link calls any such field a failure.
	if err != nil && reply["ok"] == true {
		err = nil
	}
	if err != nil {
		if e, ok := reply["error"].(string); ok && e != "" {
			return nil, errors.New(e)
		}
		return nil, errors.New("коннектор не достучался до Buff — попробуйте ещё раз")
	}
	delete(reply, "ok")
	return reply, nil
}

func (k knowledgeSurface) instructions(ctx context.Context, c caller) string {
	reply, err := k.request(ctx, "developer_knowledge", c, nil)
	if err != nil {
		return "База знаний организации сейчас недоступна: " + err.Error()
	}
	text, _ := reply["instructions"].(string)
	return text
}

func (k knowledgeSurface) call(ctx context.Context, name string, args json.RawMessage, c caller) (json.RawMessage, error) {
	switch name {
	case "list_entries":
		reply, err := k.request(ctx, "developer_knowledge", c, nil)
		if err != nil {
			return nil, err
		}
		return textResult(map[string]any{"entries": reply["entries"]})
	case "read_entry":
		var p struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(args, &p) != nil || strings.TrimSpace(p.Name) == "" {
			return nil, errors.New("knowledge_read_entry: name не задан")
		}
		reply, err := k.request(ctx, "developer_knowledge_entry", c, map[string]any{"name": strings.TrimSpace(p.Name)})
		if err != nil {
			return nil, err
		}
		return textResult(reply)
	case "write_entry":
		return k.propose(ctx, args, c)
	}
	return nil, fmt.Errorf("инструмента %q нет", name)
}

type proposal struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Scope       string `json:"scope"`
	Body        string `json:"body"`
}

// propose shows the person the whole entry and adds it only on their «yes».
func (k knowledgeSurface) propose(ctx context.Context, args json.RawMessage, c caller) (json.RawMessage, error) {
	var p proposal
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, errors.New("knowledge_write_entry: аргументы не разобраны: " + err.Error())
	}
	if c.ask == nil {
		return nil, errors.New("этот клиент не умеет спрашивать пользователя, поэтому запись не добавлена; " +
			"предложи пользователю добавить её в кабинете, в разделе «База знаний»")
	}
	answer, err := c.ask(ctx, proposalText(p, c.header.Get("X-Buff-Org")), map[string]any{
		"type": "object",
		"properties": map[string]any{"add": map[string]any{
			"type": "boolean", "title": "Добавить эту запись в базу знаний", "default": false,
		}},
		"required": []string{"add"},
	})
	if err != nil {
		return nil, err
	}
	if answer.Action != "accept" || answer.Content["add"] != true {
		return textResult(map[string]any{"written": false,
			"note": "пользователь не стал добавлять запись — не предлагай её снова и не записывай обходным путём"})
	}
	reply, err := k.request(ctx, "developer_knowledge_create", c, map[string]any{
		"name": p.Name, "title": p.Title, "description": p.Description, "type": p.Type, "scope": p.Scope, "body": p.Body,
	})
	if err != nil {
		return nil, err
	}
	entry, _ := reply["entry"].(map[string]any)
	// The person approved it, so it is theirs: say so, or the model guesses it is an AI note.
	return textResult(map[string]any{"written": true, "name": entry["name"], "scope": p.Scope,
		"author": fmt.Sprintf("%v (человек)", entry["author"])})
}

var typeLabel = map[string]string{"rule": "правило", "fact": "факт", "pitfall": "ловушка"}

// proposalText is what the person reads before deciding: the whole entry, nothing hidden.
func proposalText(p proposal, org string) string {
	scope := p.Scope
	if scope == "org" {
		scope = "вся организация"
	}
	kind := typeLabel[p.Type]
	if kind == "" {
		kind = p.Type
	}
	return fmt.Sprintf("Модель предлагает добавить запись в базу знаний организации %s.\n\n«%s» — %s, область: %s, имя: %s\n%s\n\n%s",
		org, p.Title, kind, scope, p.Name, p.Description, p.Body)
}

// textResult is a tool result the model reads: the answer as indented JSON text.
func textResult(v any) (json.RawMessage, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(b)}}})
}
