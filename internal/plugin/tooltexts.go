package plugin

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/buffsystems/buff-connector/internal/i18n"
	"github.com/buffsystems/buff-connector/internal/tooltext"
)

// ToolTextsFile is where a plugin keeps what the MODEL is told about its tools in
// one language — each tool's description and its parameters' (internal/tooltext's
// format). The plugin's SDK reads them and answers a listing in the language the
// connector names (LangMeta); the manifest's tool summaries are for a person and
// live beside it, in manifest.yaml and EnglishFile.
func ToolTextsFile(l i18n.Lang) string { return "tools." + string(l) + ".md" }

// CheckToolTexts holds a plugin's tool texts to its manifest: a plugin that words
// its tools in files has both languages, every tool of the manifest is in both
// with the same parameters, nothing describes a tool the manifest lacks, and the
// English is English. A plugin with neither file still words its tools in code,
// in one language — allowed for a publisher, refused for ours by the repository's
// test.
func CheckToolTexts(dir string, m *Manifest) error {
	texts := map[i18n.Lang]tooltext.Texts{}
	for _, l := range []i18n.Lang{i18n.RU, i18n.EN} {
		b, err := os.ReadFile(filepath.Join(dir, ToolTextsFile(l)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if texts[l], err = tooltext.Parse(b); err != nil {
			return fmt.Errorf("%s: %w", ToolTextsFile(l), err)
		}
	}
	switch len(texts) {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("тексты инструментов только на одном языке: нужны и %s, и %s",
			ToolTextsFile(i18n.RU), ToolTextsFile(i18n.EN))
	}
	names := make([]string, len(m.Tools))
	for i, t := range m.Tools {
		names[i] = t.Name
	}
	if err := tooltext.CheckPair(texts[i18n.RU], texts[i18n.EN], names); err != nil {
		return fmt.Errorf("тексты инструментов: %w", err)
	}
	return nil
}
