package main

import "testing"

func TestCheckSecrets(t *testing.T) {
	strong := "q7Lw2xV9pR4mZ8sT1yK6bN3cH5jD0fGa"
	cases := []struct {
		name              string
		jwt, email, admin string
		ok                bool
	}{
		{"random secret", strong, "", "", true},
		{"random secret and seed admin", strong, "admin@example.com", "a-strong-seed-password", true},
		{"too short", "short", "", "", false},
		{"exact placeholder", "change-me", "", "", false},
		// The value .env.example used to ship: 33 characters, so length alone
		// let it through.
		{"example placeholder", "change-me-to-a-long-random-string", "", "", false},
		{"placeholder in another case", "CHANGEME-please-use-a-long-random-string", "", "", false},
		{"seed admin without a password", strong, "admin@example.com", "", false},
		{"seed admin with the placeholder", strong, "admin@example.com", "change-me", false},
		{"seed admin with a longer placeholder", strong, "admin@example.com", "Change-Me-2026", false},
		{"seed password ignored without a seed email", strong, "", "change-me", true},
	}
	for _, c := range cases {
		err := checkSecrets(&Config{JWTSecret: c.jwt, AdminEmail: c.email, AdminPassword: c.admin})
		if (err == nil) != c.ok {
			t.Errorf("%s: got %v, want ok=%v", c.name, err, c.ok)
		}
	}
}
