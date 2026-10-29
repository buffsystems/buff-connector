package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckToolTexts(t *testing.T) {
	m := &Manifest{Tools: []ToolSpec{{Name: "search"}, {Name: "get"}}}
	write := func(t *testing.T, files map[string]string) string {
		dir := t.TempDir()
		for name, body := range files {
			_ = os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
		}
		return dir
	}
	ru := "# search\nНайти.\n## query\nчто искать\n# get\nПрочитать.\n"
	en := "# search\nFind.\n## query\nwhat to search for\n# get\nRead.\n"
	if err := CheckToolTexts(write(t, map[string]string{"tools.ru.md": ru, "tools.en.md": en}), m); err != nil {
		t.Errorf("a complete pair: %v", err)
	}
	if err := CheckToolTexts(write(t, nil), m); err != nil {
		t.Errorf("a plugin that words its tools in code: %v", err)
	}
	for name, c := range map[string]struct {
		files map[string]string
		want  string
	}{
		"one language":       {map[string]string{"tools.ru.md": ru}, "только на одном языке"},
		"a tool without en":  {map[string]string{"tools.ru.md": ru, "tools.en.md": "# search\nFind.\n## query\nq\n"}, "get: no en"},
		"a parameter":        {map[string]string{"tools.ru.md": ru, "tools.en.md": "# search\nFind.\n# get\nRead.\n"}, "search.query: no en"},
		"Russian as English": {map[string]string{"tools.ru.md": ru, "tools.en.md": strings.Replace(en, "Read.", "Прочитать.", 1)}, "get: en is not English"},
		"a broken file":      {map[string]string{"tools.ru.md": ru, "tools.en.md": "## query\nq\n"}, "tools.en.md"},
	} {
		err := CheckToolTexts(write(t, c.files), m)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}
