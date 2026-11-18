package main

import "testing"

func TestServerAdminCredsUsesProvided(t *testing.T) {
	user, pass := serverAdminCreds(" alice ", "secret")
	if user != "alice" || pass != "secret" {
		t.Fatalf("user=%q pass=%q", user, pass)
	}
}
