package main

import "testing"

func TestDeploymentURLValidation(t *testing.T) {
	for _, tc := range []struct {
		name, publicURL, appURL string
		devMode                 bool
		wantErr                 bool
	}{
		{"production https", "https://git.example.com", "https://git.example.com", false, false},
		{"production http", "http://git.example.com", "http://git.example.com", false, true},
		{"production cross origin", "https://git.example.com", "https://other.example.com", false, true},
		{"development ports", "http://127.0.0.1:8080", "http://127.0.0.1:5173", true, false},
		{"development hostname mismatch", "http://127.0.0.1:8080", "http://localhost:5173", true, true},
		{"development exposed URL", "http://192.0.2.10:8080", "http://192.0.2.10:5173", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePublicURL(tc.publicURL, tc.devMode)
			if err == nil {
				err = validateAppURL(tc.appURL, tc.publicURL, tc.devMode)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("validation err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestSSHBaseURLValidation(t *testing.T) {
	for _, raw := range []string{"ssh://git@git.example.com", "ssh://git@127.0.0.1:2222"} {
		if err := validateSSHBaseURL(raw); err != nil {
			t.Fatalf("valid SSH URL %q: %v", raw, err)
		}
	}
	for _, raw := range []string{"git@git.example.com:team/repo.git", "http://git@git.example.com", "ssh://git:secret@git.example.com", "ssh://git@git.example.com/team/repo.git", "ssh://git@git.example.com:99999"} {
		if err := validateSSHBaseURL(raw); err == nil {
			t.Fatalf("invalid SSH URL accepted: %q", raw)
		}
	}
}
