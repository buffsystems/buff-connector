// Package tooltext reads what a model is told about a set of tools — each tool's
// description and each parameter's — from a text file per language, so the
// prompt text lives in files a person reviews, not in code. The connector's own
// tools and every plugin's are written this way; a plugin's SDK reads the same
// format (sdk/buff_plugin/texts.py).
//
// The format is a small Markdown:
//
//	<!-- anything before the first tool is a note for whoever edits the file -->
//	# search_issues
//	Find issues by text. Use it when …
//
//	## query
//	What to search for
//
// A "# " heading opens a tool, "## " one of its parameters. Lines that follow
// each other join with a space (wrap them freely); a blank line starts a new
// line of the text — Buff Code shows only a description's first line in some
// views, so the first paragraph carries what the tool does and when.
package tooltext

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Tool is one tool's texts in one language.
type Tool struct {
	Description string
	Params      map[string]string
}

// Texts is one file: the tools in the order written.
type Texts struct {
	Tools map[string]*Tool
	Order []string
}

var nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// Parse reads one language's file.
func Parse(b []byte) (Texts, error) {
	out := Texts{Tools: map[string]*Tool{}}
	var tool *Tool
	var toolName, param string
	var para, paras []string
	flush := func() error {
		if len(para) > 0 {
			paras = append(paras, strings.Join(para, " "))
			para = nil
		}
		text := strings.Join(paras, "\n")
		paras = nil
		switch {
		case tool == nil:
			return nil
		case param != "":
			if text == "" {
				return fmt.Errorf("%s.%s: нет текста", toolName, param)
			}
			tool.Params[param] = text
		default:
			if text == "" {
				return fmt.Errorf("%s: нет текста", toolName)
			}
			tool.Description = text
		}
		return nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "## "):
			if tool == nil {
				return Texts{}, fmt.Errorf("параметр %q раньше первого инструмента", trimmed)
			}
			if err := flush(); err != nil {
				return Texts{}, err
			}
			param = strings.TrimSpace(line[3:])
			if !nameRe.MatchString(param) {
				return Texts{}, fmt.Errorf("%s: %q — не имя параметра", toolName, param)
			}
			if _, dup := tool.Params[param]; dup {
				return Texts{}, fmt.Errorf("%s.%s: написан дважды", toolName, param)
			}
		case strings.HasPrefix(line, "# "):
			if err := flush(); err != nil {
				return Texts{}, err
			}
			toolName, param = strings.TrimSpace(line[2:]), ""
			if !nameRe.MatchString(toolName) {
				return Texts{}, fmt.Errorf("%q — не имя инструмента", toolName)
			}
			if out.Tools[toolName] != nil {
				return Texts{}, fmt.Errorf("%s: написан дважды", toolName)
			}
			tool = &Tool{Params: map[string]string{}}
			out.Tools[toolName] = tool
			out.Order = append(out.Order, toolName)
		case tool == nil:
			// the note before the first tool
		case trimmed == "":
			if len(para) > 0 {
				paras = append(paras, strings.Join(para, " "))
				para = nil
			}
		default:
			para = append(para, trimmed)
		}
	}
	if err := flush(); err != nil {
		return Texts{}, err
	}
	if len(out.Order) == 0 {
		return Texts{}, errors.New("нет ни одного инструмента")
	}
	return out, nil
}

// Describe puts the parameters' texts into an input schema's properties.
// A text for a parameter the schema does not have is an error: the file and
// the code have drifted apart.
func (t *Tool) Describe(schema json.RawMessage) (json.RawMessage, error) {
	if len(t.Params) == 0 {
		return schema, nil
	}
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		return nil, err
	}
	props, _ := s["properties"].(map[string]any)
	for name, text := range t.Params {
		p, ok := props[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("параметр %s: его нет в схеме", name)
		}
		p["description"] = text
	}
	return json.Marshal(s)
}

// CheckPair holds two languages of the same tools to each other and to the
// tools that exist: every tool has both, with the same parameters, nothing
// describes a tool that is not there, and the English is English.
func CheckPair(ru, en Texts, tools []string) error {
	var problems []string
	exists := map[string]bool{}
	for _, name := range tools {
		exists[name] = true
		r, e := ru.Tools[name], en.Tools[name]
		if r == nil {
			problems = append(problems, name+": no ru")
		}
		if e == nil {
			problems = append(problems, name+": no en")
		}
		if r == nil || e == nil {
			continue
		}
		if hasCyrillic(e.Description) {
			problems = append(problems, name+": en is not English")
		}
		for p := range r.Params {
			if _, ok := e.Params[p]; !ok {
				problems = append(problems, name+"."+p+": no en")
			}
		}
		for p, text := range e.Params {
			if _, ok := r.Params[p]; !ok {
				problems = append(problems, name+"."+p+": no ru")
			}
			if hasCyrillic(text) {
				problems = append(problems, name+"."+p+": en is not English")
			}
		}
	}
	for _, texts := range []Texts{ru, en} {
		for name := range texts.Tools {
			if !exists[name] {
				problems = append(problems, name+": no such tool")
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("%s", strings.Join(dedupe(problems), "; "))
	}
	return nil
}

func dedupe(sorted []string) []string {
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}

func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}
