package config

// How a typed-in secret becomes a stored credential.
//
// It lives here, in the package both surfaces already depend on, because there
// are two of them: the local web page and the command line. A connector on a
// headless server is the normal case, not the exception, and a rule that only
// one of them applied would mean a connection configured by script authenticates
// differently from the same connection configured by hand.

// CredentialFor turns what an operator supplied into one stored credential.
//
// `kinds` and `header` come from the plugin's manifest — the plugin declares how
// its tracker authenticates, and the connector never guesses. The one decision
// left to the operator is WHOSE token this is: Jira Cloud takes an email and an
// API token, Bitbucket Cloud takes either a username with an app password or a
// bearer token on its own. So an account supplied means basic; absent means the
// manifest's first kind.
func CredentialFor(name string, kinds []string, header, account, file string) InstanceCredential {
	kind := "bearer"
	if len(kinds) > 0 {
		kind = kinds[0]
	}
	if account != "" && contains(kinds, "basic") {
		kind = "basic"
	}
	if kind != "basic" && kind != "password" {
		return InstanceCredential{Name: name, Kind: kind, Header: header, TokenFile: file}
	}
	// For basic auth — and for a password handed to the plugin — the secret IS the password. It is also recorded as the
	// token, because the instance fingerprint is built from the token file and a
	// connection whose secret moved must restart.
	return InstanceCredential{
		Name: name, Kind: kind, Username: account,
		TokenFile: file, PasswordFile: file,
	}
}

// CredentialOfKind is the credential when the operator CHOSE among the kinds the
// plugin offers — a Google connection signs in as a service account or as a
// person, and the form asks which. The choice must be one the plugin declared;
// an unknown one falls back to the plugin's first kind rather than being
// written down as something the host cannot attach.
func CredentialOfKind(name, kind string, kinds []string, header, account, file string) InstanceCredential {
	if kind == "" || !contains(kinds, kind) {
		return CredentialFor(name, kinds, header, account, file)
	}
	return CredentialFor(name, append([]string{kind}, kinds...), header, account, file)
}

// ReuseCredential keeps a credential whose secret was not retyped, while letting
// the account beside it change: correcting a typo in an email should not mean
// pasting the token again — and the page never receives the token, so re-saving
// the form would otherwise silently drop the correction.
func ReuseCredential(old InstanceCredential, account string) InstanceCredential {
	if account != "" && (old.Kind == "basic" || old.Kind == "password") {
		old.Username = account
	}
	return old
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
