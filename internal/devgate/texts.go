package devgate

// What the model is told about the gateway's own tools lives in files, one per language
// (tools/<surface>.<lang>.md, the internal/tooltext format): the code keeps only each tool's
// name, input schema and read-only mark, and the files give the descriptions — the tool's and
// every parameter's. The two languages are held to each other when the package loads, so a
// tool cannot ship described in one language only.

import (
	"embed"
	"encoding/json"
	"fmt"

	"github.com/buffsystems/buff-connector/internal/i18n"
	"github.com/buffsystems/buff-connector/internal/tooltext"
)

//go:embed tools/*.md
var toolFiles embed.FS

// toolSpec is a tool as the code knows it: everything but the words.
type toolSpec struct {
	name     string
	schema   string
	readOnly bool
}

// toolSet is one surface's tools in each language.
type toolSet map[i18n.Lang][]Tool

// in is the tools as a model working in l reads them.
func (s toolSet) in(l i18n.Lang) []Tool {
	if tools, ok := s[l]; ok {
		return tools
	}
	return s[i18n.RU]
}

func mustToolSet(surface string, specs []toolSpec) toolSet {
	set, err := loadToolSet(surface, specs)
	if err != nil {
		panic(fmt.Sprintf("devgate: тексты инструментов %s: %v", surface, err))
	}
	return set
}

func loadToolSet(surface string, specs []toolSpec) (toolSet, error) {
	texts := map[i18n.Lang]tooltext.Texts{}
	for _, l := range []i18n.Lang{i18n.RU, i18n.EN} {
		b, err := toolFiles.ReadFile("tools/" + surface + "." + string(l) + ".md")
		if err != nil {
			return nil, err
		}
		if texts[l], err = tooltext.Parse(b); err != nil {
			return nil, fmt.Errorf("%s: %w", l, err)
		}
	}
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.name
	}
	if err := tooltext.CheckPair(texts[i18n.RU], texts[i18n.EN], names); err != nil {
		return nil, err
	}
	set := toolSet{}
	for l, file := range texts {
		for _, s := range specs {
			text := file.Tools[s.name]
			schema, err := text.Describe(json.RawMessage(s.schema))
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", l, s.name, err)
			}
			set[l] = append(set[l], Tool{Name: s.name, Description: text.Description, InputSchema: schema, ReadOnly: s.readOnly})
		}
	}
	return set, nil
}
