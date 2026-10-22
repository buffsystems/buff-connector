package admin

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The connector's page carries the SAME mark as the app and the site. A page
// that invents its own is worse than an unbranded one here: this is the first
// thing a customer's security team opens, and a mark that matches nothing they
// have seen reads as somebody else's page.
func TestPageWearsTheProductMarkAndNotAMadeUpOne(t *testing.T) {
	if strings.Contains(string(indexHTML), `class="mark">B<`) {
		t.Error("the hand-made square-with-a-letter is back")
	}
	if !strings.Contains(string(indexHTML), `rel="icon"`) {
		t.Error("the page has no favicon, so the browser tab is blank")
	}
	// The glyph itself, not a link to one: the machine may have no route out.
	if !strings.Contains(string(indexHTML), "M12 18V5") {
		t.Error("the brand glyph is not inline in the page")
	}
}

// Served like the design system's CSS is, and for the same reason — a firewalled perimeter
// reaches no CDN — which also means unauthenticated, or the 401 page loses it.
func TestBrandAssetsAreServedWithoutACredential(t *testing.T) {
	_, srv := newAuthed(t)

	res, err := http.Get(srv.URL + "/brand/favicon.svg")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "M12 18V5") {
		t.Errorf("that is not the product mark: %.80s", body)
	}
}

// Where plugins come from is a security fact, not a debug detail: an operator
// being asked to approve an install should be able to see the source without
// reading a process's environment. It is also the only way an audit can tell a
// connector pointed at our registry from one pointed at a mirror.
func TestPluginSectionNamesWhereTheCatalogueComesFrom(t *testing.T) {
	if !strings.Contains(string(indexHTML), "catalogSource") {
		t.Error("the marketplace does not show the catalogue's address")
	}
}
