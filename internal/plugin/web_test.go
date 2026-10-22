package plugin

import (
	"net"
	"net/url"
	"strings"
	"testing"
)

// A web rule is the one egress rule whose destination the MODEL names, not the
// operator. Everything the operator can still say about it is a pattern list —
// and the patterns have to behave the way the operator expects from every other
// product with the same knob, or the list is a false sense of safety.

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("url %q: %v", s, err)
	}
	return u
}

func TestDomainPatternCoversSubdomainsButNotSiblings(t *testing.T) {
	p, err := ParseDomainPattern("example.com")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, ok := range []string{
		"https://example.com/", "https://docs.example.com/x", "HTTP://EXAMPLE.COM/a?b",
		"https://example.com:8443/",
	} {
		if !p.Matches(mustURL(t, ok)) {
			t.Errorf("%s should match example.com", ok)
		}
	}
	for _, no := range []string{
		"https://notexample.com/", "https://example.com.evil.net/", "https://example.org/",
	} {
		if p.Matches(mustURL(t, no)) {
			t.Errorf("%s must NOT match example.com", no)
		}
	}
	// A specific subdomain is exactly that subdomain — not the parent, not a sibling.
	docs, _ := ParseDomainPattern("docs.example.com")
	if docs.Matches(mustURL(t, "https://example.com/")) || docs.Matches(mustURL(t, "https://api.example.com/")) {
		t.Error("docs.example.com widened to the parent or a sibling")
	}
	if !docs.Matches(mustURL(t, "https://docs.example.com/guide")) {
		t.Error("docs.example.com should match itself")
	}
}

func TestDomainPatternPathIsAPrefixWithSegmentWildcards(t *testing.T) {
	blog, _ := ParseDomainPattern("example.com/blog")
	if !blog.Matches(mustURL(t, "https://example.com/blog/post-1")) || !blog.Matches(mustURL(t, "https://example.com/blog")) {
		t.Error("a path entry matches anything after it")
	}
	if blog.Matches(mustURL(t, "https://example.com/blogs")) || blog.Matches(mustURL(t, "https://example.com/")) {
		t.Error("a path entry is a segment, not a string prefix")
	}
	star, _ := ParseDomainPattern("example.com/*/articles")
	if !star.Matches(mustURL(t, "https://example.com/2026/articles/one")) {
		t.Error("* stands for one segment")
	}
	if star.Matches(mustURL(t, "https://example.com/articles")) || star.Matches(mustURL(t, "https://example.com/a/b/articles")) {
		t.Error("* is exactly one segment")
	}
	any, _ := ParseDomainPattern("example.com/*")
	if !any.Matches(mustURL(t, "https://example.com/whatever")) || any.Matches(mustURL(t, "https://example.com/")) {
		t.Error("/* needs at least one segment")
	}
}

func TestDomainPatternRejectsWhatWouldSilentlyMeanSomethingElse(t *testing.T) {
	for _, bad := range []string{
		"https://example.com", // a scheme — the entry is a domain, not an address
		"*.example.com",       // a wildcard in the host: subdomains are already included
		"ex*.com",             // ditto
		"аmazon.com",          // Cyrillic а — a homograph is a different domain
		"",                    // nothing
		".example.com",        // a leading dot is not a domain
		"example.com/a b",     // whitespace in a path
	} {
		if _, err := ParseDomainPattern(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	// Ports are not part of a domain rule: an operator allowing example.com
	// means the site, on whatever port it answers.
	if _, err := ParseDomainPattern("example.com:8443"); err == nil {
		t.Error("a port in a domain entry must be refused: rules are about sites, not sockets")
	}
}

func TestWebPolicyAllowListWinsOverDenyList(t *testing.T) {
	pol, err := NewWebPolicy([]string{"docs.python.org", "example.com/blog"}, []string{"example.com"}, nil)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	if err := pol.Allows(mustURL(t, "https://docs.python.org/3/")); err != nil {
		t.Errorf("allowed entry refused: %v", err)
	}
	if err := pol.Allows(mustURL(t, "https://example.com/blog/x")); err != nil {
		t.Errorf("an allow entry is not undone by a deny entry — the allow list alone is in force: %v", err)
	}
	if err := pol.Allows(mustURL(t, "https://example.com/pricing")); err == nil {
		t.Error("outside the allow list must be refused")
	}
	if err := pol.Allows(mustURL(t, "https://wikipedia.org/")); err == nil {
		t.Error("with an allow list, everything else is refused")
	}
}

func TestWebPolicyDenyListAloneIsOpenExceptTheList(t *testing.T) {
	pol, _ := NewWebPolicy(nil, []string{"pastebin.com", "example.com/private"}, nil)
	if err := pol.Allows(mustURL(t, "https://wikipedia.org/wiki/Go")); err != nil {
		t.Errorf("an open policy refused a public site: %v", err)
	}
	if err := pol.Allows(mustURL(t, "https://pastebin.com/raw/x")); err == nil {
		t.Error("the deny list must hold")
	}
	if err := pol.Allows(mustURL(t, "https://example.com/private/x")); err == nil {
		t.Error("a deny entry with a path must hold on that path")
	}
	if err := pol.Allows(mustURL(t, "https://example.com/public")); err != nil {
		t.Errorf("a deny entry with a path must not cover the whole site: %v", err)
	}
}

func TestWebPolicyOnlySpeaksHTTP(t *testing.T) {
	pol, _ := NewWebPolicy(nil, nil, nil)
	for _, bad := range []string{"ftp://example.com/x", "file:///etc/passwd", "gopher://x/", "javascript:alert(1)"} {
		if err := pol.Allows(mustURL(t, bad)); err == nil {
			t.Errorf("%s must be refused", bad)
		}
	}
	if err := pol.Allows(mustURL(t, "http://example.com/")); err != nil {
		t.Errorf("plain http is a web address: %v", err)
	}
}

func TestWebPolicyContentTypes(t *testing.T) {
	open, _ := NewWebPolicy(nil, nil, nil)
	if err := open.AllowsType("application/octet-stream"); err != nil {
		t.Errorf("no type list means any type: %v", err)
	}
	pol, err := NewWebPolicy(nil, nil, []string{"text/html", "application/json", "text/*"})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	for _, ok := range []string{"text/html; charset=utf-8", "APPLICATION/JSON", "text/plain", "text/csv"} {
		if err := pol.AllowsType(ok); err != nil {
			t.Errorf("%s should pass: %v", ok, err)
		}
	}
	for _, no := range []string{"application/pdf", "image/png", "", "application/octet-stream"} {
		if err := pol.AllowsType(no); err == nil {
			t.Errorf("%q must be refused", no)
		}
	}
	if _, err := NewWebPolicy(nil, nil, []string{"html"}); err == nil {
		t.Error("a type entry that is not a media type must be refused at configuration time")
	}
}

func TestOnlyPublicAddressesArePublic(t *testing.T) {
	for _, ip := range []string{
		"127.0.0.1", "::1", "10.1.2.3", "172.16.5.5", "192.168.1.1", "169.254.169.254",
		"0.0.0.0", "::", "100.64.1.1", "fc00::1", "fe80::1", "224.0.0.1", "ff02::1",
		"::ffff:10.0.0.1", "0.1.2.3",
	} {
		if isPublicIP(net.ParseIP(ip)) {
			t.Errorf("%s is not a public address", ip)
		}
	}
	for _, ip := range []string{"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946", "8.8.8.8", "1.1.1.1"} {
		if !isPublicIP(net.ParseIP(ip)) {
			t.Errorf("%s is public", ip)
		}
	}
}

// The resolver is injected so the test decides what a name means. The point:
// ONE private address among several public ones is enough to refuse, because
// the attacker picks which one the dial lands on.
func TestPublicAddressForRefusesNamesThatResolveInside(t *testing.T) {
	lookup := func(host string) ([]net.IP, error) {
		switch host {
		case "public.example":
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		case "mixed.example":
			return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("10.0.0.5")}, nil
		case "inside.example":
			return []net.IP{net.ParseIP("192.168.1.10")}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host}
	}
	if ip, err := publicAddressFor("public.example", lookup); err != nil || ip.String() != "93.184.216.34" {
		t.Fatalf("public name: %v %v", ip, err)
	}
	for _, name := range []string{"mixed.example", "inside.example", "localhost"} {
		if _, err := publicAddressFor(name, lookup); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s must be refused and named: %v", name, err)
		}
	}
	// A literal address gets the same treatment without a lookup.
	if _, err := publicAddressFor("169.254.169.254", lookup); err == nil {
		t.Error("the metadata address must be refused")
	}
	if ip, err := publicAddressFor("8.8.8.8", lookup); err != nil || ip.String() != "8.8.8.8" {
		t.Errorf("a public literal passes: %v %v", ip, err)
	}
}
