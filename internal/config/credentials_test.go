package config

import "testing"

// Jira Cloud and Bitbucket Cloud authenticate with basic auth: an account and a
// token, not a token alone. Until this existed the page had one field, the
// account was left empty, and every call went out as `Basic OjxUT0tFTj4=` — a
// 401 that looked like a wrong token.
func TestBasicCredentialCarriesTheAccount(t *testing.T) {
	got := CredentialFor("api", []string{"basic"}, "", "petrov@example.com", "/secrets/api.token")
	if got.Kind != "basic" {
		t.Fatalf("kind = %q, want basic", got.Kind)
	}
	if got.Username != "petrov@example.com" {
		t.Errorf("username = %q", got.Username)
	}
	// The secret IS the password for basic auth; it is also kept as the token so
	// the instance fingerprint still notices when the file behind it changes.
	if got.PasswordFile != "/secrets/api.token" || got.TokenFile != "/secrets/api.token" {
		t.Errorf("files = %q / %q", got.TokenFile, got.PasswordFile)
	}
}

func TestBearerCredentialIsUnchangedByAnEmptyAccount(t *testing.T) {
	got := CredentialFor("api", []string{"bearer"}, "", "", "/secrets/api.token")
	if got.Kind != "bearer" || got.Username != "" || got.PasswordFile != "" {
		t.Fatalf("credential = %+v", got)
	}
	if got.TokenFile != "/secrets/api.token" {
		t.Errorf("token file = %q", got.TokenFile)
	}
}

// A plugin that accepts either says so in its manifest. Which one it is, is told
// by whether the operator named an account — Bitbucket Cloud takes an app
// password with a username and an API token without one.
func TestKindFollowsWhetherAnAccountWasNamed(t *testing.T) {
	kinds := []string{"bearer", "basic"}
	if got := CredentialFor("api", kinds, "", "buffbot", "/f"); got.Kind != "basic" {
		t.Errorf("with an account: kind = %q", got.Kind)
	}
	if got := CredentialFor("api", kinds, "", "", "/f"); got.Kind != "bearer" {
		t.Errorf("without an account: kind = %q", got.Kind)
	}
}

// An account typed into a plugin that authenticates any other way is a mistake
// worth ignoring rather than obeying: the kind stays what the manifest says.
func TestAnAccountDoesNotTurnAHeaderCredentialIntoBasic(t *testing.T) {
	got := CredentialFor("api", []string{"header"}, "X-Redmine-API-Key", "typed-by-mistake", "/f")
	if got.Kind != "header" || got.Header != "X-Redmine-API-Key" {
		t.Fatalf("credential = %+v", got)
	}
	if got.Username != "" {
		t.Errorf("username = %q, want none", got.Username)
	}
}

func TestAnAccountCanBeCorrectedWithoutRetypingTheToken(t *testing.T) {
	old := CredentialFor("api", []string{"basic"}, "", "typo@example.com", "/secrets/api.token")
	got := ReuseCredential(old, "right@example.com")
	if got.Username != "right@example.com" || got.TokenFile != "/secrets/api.token" {
		t.Fatalf("credential = %+v", got)
	}
	if same := ReuseCredential(old, ""); same.Username != "typo@example.com" {
		t.Errorf("an empty account must leave the stored one alone: %+v", same)
	}
}

// A plugin that offers several kinds — a service account OR a signed-in person —
// lets the operator choose; the choice is kept, and a choice the plugin never
// offered is not.
func TestAChosenKindWinsOverTheDeclarationOrder(t *testing.T) {
	kinds := []string{"service_account", "oauth2"}
	if got := CredentialOfKind("google", "oauth2", kinds, "", "", "/f"); got.Kind != "oauth2" {
		t.Fatalf("kind = %q, want the chosen oauth2", got.Kind)
	}
	if got := CredentialOfKind("google", "", kinds, "", "", "/f"); got.Kind != "service_account" {
		t.Fatalf("kind = %q, want the first declared when nothing was chosen", got.Kind)
	}
	if got := CredentialOfKind("google", "bearer", kinds, "", "", "/f"); got.Kind != "service_account" {
		t.Fatalf("kind = %q, an undeclared choice must not be written down", got.Kind)
	}
}

// A password credential is an account and a password the PLUGIN receives (SMB
// is not HTTP, so the host cannot attach it). It is stored like basic auth —
// the secret is the password, and it also stands as the token so a changed
// secret restarts the connection.
func TestPasswordCredentialCarriesAccountAndPassword(t *testing.T) {
	got := CredentialFor("share", []string{"password"}, "", `CORP\svc`, "/secrets/share.token")
	if got.Kind != "password" || got.Username != `CORP\svc` {
		t.Fatalf("got %+v", got)
	}
	if got.PasswordFile != "/secrets/share.token" || got.TokenFile != "/secrets/share.token" {
		t.Fatalf("files: %+v", got)
	}
	if got := ReuseCredential(got, `CORP\other`); got.Username != `CORP\other` {
		t.Fatalf("the account beside a kept password may change: %+v", got)
	}
}
