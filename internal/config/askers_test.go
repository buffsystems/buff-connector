package config

import "testing"

// Who may ask the bot from a comment is the operator's list on the connection
// (cloud-front#476). An empty list and no «members» box means everyone, as
// before; otherwise a request that matches no rule is refused before it costs
// anything.
func TestAskerVerdict(t *testing.T) {
	rules := []string{"petrov", "a.smirnova@company.ru", "@corp.ru", `/^dev-.+@company\.ru$/`}
	cases := []struct {
		name         string
		rules        []string
		members      bool
		login, email string
		want         AskVerdict
	}{
		{"no rules and no box: everyone", nil, false, "anyone", "", AskOpen},
		{"a login", rules, false, "Petrov", "", AskAllowed},
		{"an exact address", rules, false, "anna", "A.Smirnova@company.ru", AskAllowed},
		{"a domain", rules, false, "", "ivanov@corp.ru", AskAllowed},
		{"a domain is not a suffix of another domain", rules, false, "", "ivanov@notcorp.ru", AskDenied},
		{"a regular expression on the address", rules, false, "", "dev-ops@company.ru", AskAllowed},
		{"nothing matches", rules, false, "guest", "guest@vendor.com", AskDenied},
		{"no identity at all", rules, false, "", "", AskDenied},
		{"members only: the platform decides", nil, true, "guest", "guest@vendor.com", AskMembers},
		{"a rule wins before the members check", rules, true, "petrov", "", AskAllowed},
		{"blank lines are not rules", []string{"", "  "}, false, "anyone", "", AskOpen},
	}
	for _, c := range cases {
		if got := AskerVerdict(c.rules, c.members, c.login, c.email); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// A broken expression is the operator's typo; it is refused when the
// connection is saved, not discovered as a silent «nobody may ask».
func TestValidateAskers(t *testing.T) {
	if err := ValidateAskers([]string{"petrov", `/^dev-.+$/`}); err != nil {
		t.Fatalf("valid rules refused: %v", err)
	}
	if err := ValidateAskers([]string{"/(unclosed/"}); err == nil {
		t.Fatal("a broken regular expression was accepted")
	}
}
