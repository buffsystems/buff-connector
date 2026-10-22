package config

import (
	"fmt"
	"regexp"
	"strings"
)

// Who may ask the bot from a comment in the customer's own system — the
// operator's list on the connection (cloud-front#476). One rule per line:
//
//	petrov                 a login, as the system names its user
//	petrov@company.ru      an address
//	@company.ru            every address on that domain
//	/^dev-.+@company\.ru$/ a regular expression, tried on the login and the address
//
// The connector checks the rules; «members of the organisation» is a box beside
// them that only the platform can answer, because only it knows who the members
// are and which domains its single sign-on vouches for.

// AskVerdict is what the connector found, carried with the request.
type AskVerdict string

const (
	AskOpen    AskVerdict = "open"    // no list and no box: anyone, as before
	AskAllowed AskVerdict = "allowed" // a rule matched
	AskMembers AskVerdict = "members" // no rule matched; the platform checks membership
	AskDenied  AskVerdict = "denied"  // no rule matched and membership is not asked
)

// AskerVerdict decides a request from its author's login and address.
func AskerVerdict(rules []string, members bool, login, email string) AskVerdict {
	live := liveRules(rules)
	if len(live) == 0 && !members {
		return AskOpen
	}
	login, email = strings.ToLower(strings.TrimSpace(login)), strings.ToLower(strings.TrimSpace(email))
	for _, r := range live {
		if matchAsker(r, login, email) {
			return AskAllowed
		}
	}
	if members {
		return AskMembers
	}
	return AskDenied
}

// ValidateAskers refuses a rule that can never work, so a typo is an error when
// the connection is saved rather than a silent «nobody may ask».
func ValidateAskers(rules []string) error {
	for _, r := range liveRules(rules) {
		if isRegexRule(r) {
			if _, err := regexp.Compile("(?i)" + r[1:len(r)-1]); err != nil {
				return fmt.Errorf("правило %s: не регулярное выражение: %v", r, err)
			}
		}
	}
	return nil
}

func liveRules(rules []string) []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}

func isRegexRule(r string) bool {
	return len(r) > 2 && strings.HasPrefix(r, "/") && strings.HasSuffix(r, "/")
}

func matchAsker(rule, login, email string) bool {
	switch {
	case isRegexRule(rule):
		re, err := regexp.Compile("(?i)" + rule[1:len(rule)-1])
		return err == nil && ((login != "" && re.MatchString(login)) || (email != "" && re.MatchString(email)))
	case strings.HasPrefix(rule, "@"):
		return email != "" && strings.HasSuffix(email, strings.ToLower(rule))
	case strings.Contains(rule, "@"):
		return email != "" && email == strings.ToLower(rule)
	default:
		return login != "" && login == strings.ToLower(rule)
	}
}
