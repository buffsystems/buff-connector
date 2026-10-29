package tooltext

import (
	"encoding/json"
	"strings"
	"testing"
)

const sample = `<!-- A note for whoever edits this file: not part of any text. -->

# search
Find issues by text. Use it when an issue
is mentioned.
Then read one with get.

## query
What to search for

## limit
How many; 0 — 20

# get
Read one issue.
`

func TestParse(t *testing.T) {
	got, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Order, ",") != "search,get" {
		t.Errorf("order %v", got.Order)
	}
	s := got.Tools["search"]
	// A soft-wrapped line joins with a space; a new paragraph is a new line.
	if want := "Find issues by text. Use it when an issue is mentioned. Then read one with get."; s.Description != want {
		t.Errorf("description %q", s.Description)
	}
	if s.Params["query"] != "What to search for" || s.Params["limit"] != "How many; 0 — 20" {
		t.Errorf("params %v", s.Params)
	}
	if got.Tools["get"].Description != "Read one issue." || len(got.Tools["get"].Params) != 0 {
		t.Errorf("get %+v", got.Tools["get"])
	}
}

func TestParagraphsBecomeLines(t *testing.T) {
	got, err := Parse([]byte("# t\nFirst line, shown alone.\n\nThe rest of the\ncontract.\n"))
	if err != nil {
		t.Fatal(err)
	}
	if d := got.Tools["t"].Description; d != "First line, shown alone.\nThe rest of the contract." {
		t.Errorf("%q", d)
	}
}

func TestParseRefuses(t *testing.T) {
	for name, text := range map[string]string{
		"a parameter before any tool": "## query\nx\n",
		"a tool twice":                "# a\nx\n# a\ny\n",
		"a parameter twice":           "# a\nx\n## p\ny\n## p\nz\n",
		"a tool with no text":         "# a\n## p\ny\n",
		"a parameter with no text":    "# a\nx\n## p\n",
		"a name that is no name":      "# two words\nx\n",
		"nothing at all":              "just a note\n",
	} {
		if _, err := Parse([]byte(text)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDescribeSchema(t *testing.T) {
	texts, _ := Parse([]byte(sample))
	schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer"}},"required":["query"]}`)
	out, err := texts.Tools["search"].Describe(schema)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	if v.Properties["query"].Description != "What to search for" || v.Properties["limit"].Type != "integer" || len(v.Required) != 1 {
		t.Errorf("%s", out)
	}
	// A text for a parameter the schema lacks is a mistake, not a no-op.
	if _, err := texts.Tools["search"].Describe(json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)); err == nil {
		t.Error("a text for a missing parameter was accepted")
	}
}

func TestCheckPair(t *testing.T) {
	ru, _ := Parse([]byte("# search\nНайти.\n\n## query\nЧто искать\n# get\nПрочитать.\n"))
	en, _ := Parse([]byte("# search\nFind.\n\n## query\nWhat to search for\n# get\nRead.\n"))
	if err := CheckPair(ru, en, []string{"search", "get"}); err != nil {
		t.Errorf("a complete pair: %v", err)
	}
	cases := map[string]struct {
		en    string
		tools []string
		want  string
	}{
		"a tool without English":      {"# search\nFind.\n## query\nq\n", []string{"search", "get"}, "get: no en"},
		"a parameter without English": {"# search\nFind.\n# get\nRead.\n", []string{"search", "get"}, "search.query: no en"},
		"English that is not":         {"# search\nНайти.\n## query\nq\n# get\nRead.\n", []string{"search", "get"}, "search: en is not English"},
		"a tool the plugin lacks":     {"# search\nFind.\n## query\nq\n# get\nRead.\n# gone\nx\n", []string{"search", "get"}, "gone: no such tool"},
		"a tool nobody described":     {"# search\nFind.\n## query\nq\n# get\nRead.\n", []string{"search", "get", "new"}, "new: no ru"},
	}
	for name, c := range cases {
		e, err := Parse([]byte(c.en))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		err = CheckPair(ru, e, c.tools)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}
