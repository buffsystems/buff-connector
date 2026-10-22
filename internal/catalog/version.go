package catalog

import "strings"

// Version semantics for the whole connector.
//
// The catalogue is what says which versions exist, so it is also what says which
// of two is newer and which one an operator may move to. Everything else — the
// local page, the upgrade check, the report that tells the platform an update is
// out — asks here, so those three can never disagree (#302).

// Newer reports whether `candidate` is a later version than `have`, comparing
// numbers rather than text: "1.10.0" is newer than "1.9.9", which a string
// comparison gets backwards. A leading "v" (the connector writes its own version
// that way) is ignored, and an empty version is never newer than anything —
// a badge pointing at nowhere is worse than no badge.
func Newer(candidate, have string) bool {
	if strings.TrimSpace(candidate) == "" || strings.TrimSpace(have) == "" {
		return false
	}
	c, h := parts(candidate), parts(have)
	// A version with no numbers in it — "dev", a branch name — cannot be placed
	// on the line at all, so nothing is claimed about it in either direction.
	if len(c) == 0 || len(h) == 0 {
		return false
	}
	for i := 0; i < len(c) || i < len(h); i++ {
		a, b := at(c, i), at(h, i)
		if a != b {
			return a > b
		}
	}
	return false
}

// Latest is the newest version of a plugin an operator may install today, or ""
// when the catalogue does not carry it. A WITHDRAWN version is skipped: telling
// someone to upgrade to what we took back is worse than saying nothing.
func (i Index) Latest(plugin string) string {
	latest := ""
	for _, e := range i.Plugins {
		if e.Name != plugin {
			continue
		}
		for _, v := range e.Versions {
			if v.Status == "withdrawn" {
				continue
			}
			if Newer(v.Version, latest) || latest == "" {
				latest = v.Version
			}
		}
	}
	return latest
}

func parts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	out := []int{}
	for _, f := range strings.Split(v, ".") {
		n := 0
		for _, r := range f {
			if r < '0' || r > '9' {
				n = -1
				break
			}
			n = n*10 + int(r-'0')
		}
		if n < 0 {
			break
		}
		out = append(out, n)
	}
	return out
}

func at(v []int, i int) int {
	if i < len(v) {
		return v[i]
	}
	return 0
}

// Update is the version this plugin should move to, or "" when it is already on
// the newest one the catalogue carries. This is the whole rule behind the badge:
// never downwards, never to a withdrawn version, never to nothing.
func (i Index) Update(plugin, have string) string {
	latest := i.Latest(plugin)
	if Newer(latest, have) {
		return latest
	}
	return ""
}

// CoreUpdate is the same question for the connector itself.
func (i Index) CoreUpdate(have string) string {
	if Newer(i.Connector, have) {
		return i.Connector
	}
	return ""
}
