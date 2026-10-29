package plugin

import (
	"fmt"
	"mime"
	"net"
	"net/url"
	"strings"

	"github.com/buffsystems/buff-connector/internal/problem"
)

// A web rule is the egress rule whose destination the MODEL names. Every other
// rule points at a system the operator configured; this one points at the
// internet, and what the operator gets instead of an address is a policy:
//
//   - an allow list, or a deny list — written the way every product with the
//     same knob writes it (bare domain, subdomains included, an optional path
//     with `*` for one segment, no scheme, no wildcard in the host, ASCII only,
//     because a Cyrillic а in "аmazon.com" is a different site that looks the same);
//   - the content types the model may be handed;
//   - and one thing the operator cannot switch off: the address must be PUBLIC.
//     A name that resolves inside the perimeter — loopback, a private range,
//     link-local where every cloud keeps its credential service — is refused,
//     and the connection is made to the very address that was checked, so a
//     name cannot change its mind between the check and the dial.
//
// Nothing here truncates or follows a redirect; the HTTP gate keeps both rules.

// DomainPattern is one allow- or deny-list entry, parsed.
type DomainPattern struct {
	Host     string
	Segments []string
	raw      string
}

// ParseDomainPattern reads an entry the way the operator wrote it — and refuses
// the forms that would silently mean something else.
func ParseDomainPattern(s string) (DomainPattern, error) {
	raw := strings.TrimSpace(s)
	entry := strings.ToLower(raw)
	if entry == "" {
		return DomainPattern{}, problem.New("web_entry_empty")
	}
	if strings.Contains(entry, "://") {
		return DomainPattern{}, problem.New("web_entry_scheme", raw)
	}
	for _, r := range entry {
		if r > 0x7f {
			return DomainPattern{}, problem.New("web_entry_ascii", raw)
		}
		if r == ' ' || r == '\t' {
			return DomainPattern{}, problem.New("web_entry_space", raw)
		}
	}
	host, path, _ := strings.Cut(entry, "/")
	if host == "" || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return DomainPattern{}, problem.New("web_entry_not_domain", raw)
	}
	if strings.Contains(host, "*") {
		return DomainPattern{}, problem.New("web_entry_wildcard", raw)
	}
	if strings.Contains(host, ":") {
		return DomainPattern{}, problem.New("web_entry_port", raw)
	}
	for _, r := range host {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return DomainPattern{}, problem.New("web_entry_not_domain", raw)
		}
	}
	return DomainPattern{Host: host, Segments: segments(path), raw: raw}, nil
}

func segments(path string) []string {
	var out []string
	for _, s := range strings.Split(path, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Matches says whether the URL falls under this entry.
func (p DomainPattern) Matches(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	if host != p.Host && !strings.HasSuffix(host, "."+p.Host) {
		return false
	}
	got := segments(u.Path)
	if len(got) < len(p.Segments) {
		return false
	}
	for i, want := range p.Segments {
		if want != "*" && want != got[i] {
			return false
		}
	}
	return true
}

func (p DomainPattern) String() string { return p.raw }

// WebPolicy is what the operator decided about the internet.
type WebPolicy struct {
	Allow []DomainPattern
	Deny  []DomainPattern
	Types []string
}

// NewWebPolicy parses the operator's lists. A non-empty allow list is the whole
// policy; the deny list only speaks when there is no allow list.
func NewWebPolicy(allow, deny, types []string) (*WebPolicy, error) {
	pol := &WebPolicy{}
	var err error
	if pol.Allow, err = parsePatterns(allow); err != nil {
		return nil, problem.Wrap("web_allow_list", err)
	}
	if pol.Deny, err = parsePatterns(deny); err != nil {
		return nil, problem.Wrap("web_deny_list", err)
	}
	for _, t := range types {
		mt, _, perr := mime.ParseMediaType(strings.TrimSpace(t))
		if perr != nil || !strings.Contains(mt, "/") {
			return nil, problem.New("web_content_type", t)
		}
		pol.Types = append(pol.Types, strings.ToLower(mt))
	}
	return pol, nil
}

func parsePatterns(list []string) ([]DomainPattern, error) {
	out := make([]DomainPattern, 0, len(list))
	for _, s := range list {
		p, err := ParseDomainPattern(s)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Allows checks one address against the lists — and against the only two
// schemes that are a web address at all.
func (w *WebPolicy) Allows(u *url.URL) error {
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("%s is not a web address (only http and https)", u.String())
	}
	if u.Hostname() == "" {
		return fmt.Errorf("%s names no host", u.String())
	}
	if len(w.Allow) > 0 {
		for _, p := range w.Allow {
			if p.Matches(u) {
				return nil
			}
		}
		return fmt.Errorf("%s is outside the allowed sites", u.Host)
	}
	for _, p := range w.Deny {
		if p.Matches(u) {
			return fmt.Errorf("%s is on the blocked list (%s)", u.Host, p)
		}
	}
	return nil
}

// AllowsType checks a response's Content-Type against the operator's list. No
// list means the model may be handed whatever came back.
func (w *WebPolicy) AllowsType(contentType string) error {
	if len(w.Types) == 0 {
		return nil
	}
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return fmt.Errorf("the response declares no usable content type (%q)", contentType)
	}
	mt = strings.ToLower(mt)
	major, _, _ := strings.Cut(mt, "/")
	for _, t := range w.Types {
		if t == mt || t == major+"/*" {
			return nil
		}
	}
	return fmt.Errorf("content type %s is not among the allowed types", mt)
}

// Label is how the consent screen and the report describe this policy: what an
// operator reads instead of a host name.
func (w *WebPolicy) Label() string {
	names := func(ps []DomainPattern) string {
		out := make([]string, 0, len(ps))
		for _, p := range ps {
			out = append(out, p.String())
		}
		return strings.Join(out, ", ")
	}
	switch {
	case len(w.Allow) > 0:
		return fmt.Sprintf("интернет: %s", names(w.Allow))
	case len(w.Deny) > 0:
		return fmt.Sprintf("интернет, кроме: %s", names(w.Deny))
	}
	return "интернет"
}

// isPublicIP is the one rule the operator cannot relax: nothing inside.
func isPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() ||
		ip.IsUnspecified() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 0: // "this" network
			return false
		case v4[0] == 100 && v4[1]&0xc0 == 64: // carrier-grade NAT, 100.64/10
			return false
		}
	}
	return true
}

type lookupFunc func(host string) ([]net.IP, error)

// publicAddressFor resolves a name and returns the address the connection must
// be made to. ONE inside address among the answers is enough to refuse: the
// attacker chooses which answer the dial would land on.
func publicAddressFor(host string, lookup lookupFunc) (net.IP, error) {
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		if !isPublicIP(ip) {
			return nil, fmt.Errorf("%s is not a public address", host)
		}
		return ip, nil
	}
	ips, err := lookup(host)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%s resolves to nothing", host)
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return nil, fmt.Errorf("%s resolves to %s, which is not a public address", host, ip)
		}
	}
	return ips[0], nil
}
