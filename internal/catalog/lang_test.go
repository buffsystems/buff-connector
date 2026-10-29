package catalog

import (
	"testing"

	"github.com/buffsystems/buff-connector/internal/i18n"
)

func TestIndexInLang(t *testing.T) {
	idx := &Index{Plugins: []Entry{
		{Name: "gitlab", Summary: "GitLab — вопросы", SummaryEN: "GitLab — questions", Category: "интеграция", CategoryEN: "integration",
			Versions: []Version{{Version: "1.1.1", Tools: []Tool{{Name: "a", Summary: "Найти", SummaryEN: "Find"}, {Name: "b", Summary: "Прочитать"}}}}},
		// An entry from an older publisher has no English: shown as it is.
		{Name: "old", Summary: "Старый", Category: "инструменты"},
	}}
	en := idx.InLang(i18n.EN)
	g := en.Plugins[0]
	if g.Summary != "GitLab — questions" || g.Category != "integration" || g.Versions[0].Tools[0].Summary != "Find" || g.Versions[0].Tools[1].Summary != "Прочитать" {
		t.Errorf("English entry = %+v", g)
	}
	if en.Plugins[1].Summary != "Старый" {
		t.Errorf("an entry without English = %+v", en.Plugins[1])
	}
	if idx.Plugins[0].Summary != "GitLab — вопросы" || idx.Plugins[0].Versions[0].Tools[0].Summary != "Найти" {
		t.Error("InLang changed the index it was given")
	}
	if idx.InLang(i18n.RU) != idx {
		t.Error("Russian is the index as it is")
	}
}
