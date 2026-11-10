package main

import "testing"

// #768: a standalone connector read its catalogue from oci://registry.buff.systems/buff/plugins
// — a repository the release never fills (the catalogue moved to the site, #619). It saw no
// plugins and never learned that a new connector is out. The default is where the release
// publishes it.
func TestDefaultCatalogIsWhereTheReleasePublishes(t *testing.T) {
	if catalogBase != "https://buff.systems/downloads/plugins" {
		t.Fatalf("catalogBase = %q", catalogBase)
	}
}
