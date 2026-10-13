package sshtransport

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"fithub.local/fithub/internal/store"
)

func generateKey(t *testing.T, keyType string, bits string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "id_"+keyType)
	args := []string{"-q", "-t", keyType, "-N", "", "-C", "untrusted-comment", "-f", path}
	if bits != "" {
		args = append(args, "-b", bits)
	}
	if out, err := exec.Command("ssh-keygen", args...).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	public, err := os.ReadFile(path + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	return string(public)
}

func TestParseSSHKeysGeneratedByOpenSSH(t *testing.T) {
	for _, tc := range []struct{ keyType, bits string }{
		{"ed25519", ""}, {"ecdsa", "256"}, {"rsa", "3072"},
	} {
		t.Run(tc.keyType, func(t *testing.T) {
			public := generateKey(t, tc.keyType, tc.bits)
			parsed, err := ParsePublicKey(public)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(parsed.PublicKey, "untrusted-comment") || !strings.HasPrefix(parsed.Fingerprint, "SHA256:") {
				t.Fatalf("key was not canonicalized: %+v", parsed)
			}
			path := filepath.Join(t.TempDir(), "key.pub")
			if err := os.WriteFile(path, []byte(public), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command("ssh-keygen", "-lf", path).Output()
			if err != nil {
				t.Fatal(err)
			}
			if fields := strings.Fields(string(out)); len(fields) < 2 || fields[1] != parsed.Fingerprint {
				t.Fatalf("fingerprint mismatch with ssh-keygen: %s != %s", out, parsed.Fingerprint)
			}
			line, err := AuthorizedKeyLine(store.SSHKey{
				ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", KeyType: parsed.Type,
				PublicKey: parsed.PublicKey, Fingerprint: parsed.Fingerprint,
			}, "/usr/local/bin/fithub-ssh", "/etc/fithub/ssh.json")
			if err != nil || !strings.HasPrefix(line, `command="/usr/local/bin/fithub-ssh serve --config /etc/fithub/ssh.json --key-id aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",restrict `) {
				t.Fatalf("invalid authorized_keys line: %q, %v", line, err)
			}
			if strings.Contains(line, "untrusted-comment") {
				t.Fatalf("untrusted comment leaked into command: %q", line)
			}
		})
	}
	weakRSA := generateKey(t, "rsa", "2048")
	if _, err := ParsePublicKey(weakRSA); err == nil {
		t.Fatal("2048-bit RSA key was accepted")
	}
}

func TestRejectKeyOptionsAndMultipleKeys(t *testing.T) {
	public := generateKey(t, "ed25519", "")
	for _, candidate := range []string{
		`command="/bin/sh" ` + public,
		public + public,
		"not-a-key",
	} {
		if _, err := ParsePublicKey(candidate); err == nil {
			t.Fatalf("unsafe key was accepted: %q", candidate)
		}
	}
}

func TestParseOriginalGitCommand(t *testing.T) {
	for _, good := range []string{
		"git-upload-pack 'engineering/demo.git'",
		"git-receive-pack '/engineering/demo.git'",
	} {
		if _, err := ParseOriginalCommand(good); err != nil {
			t.Fatalf("valid command %q: %v", good, err)
		}
	}
	for _, bad := range []string{
		"", "sh", "git-upload-archive 'engineering/demo.git'",
		"git-upload-pack '../../etc/passwd'", "git-upload-pack 'engineering/../demo.git'",
		"git-upload-pack 'engineering/demo.git'; id", "git-upload-pack 'engineering/demo.git' extra",
		"git-receive-pack 'engineering/demo.git'\nwhoami", "git-upload-pack 'engineering/demo.git/sub'",
		"git-upload-pack 'engineering/demo.git' && whoami",
	} {
		if _, err := ParseOriginalCommand(bad); err == nil {
			t.Fatalf("unsafe command was accepted: %q", bad)
		}
	}
}

func TestSSHGatewayConfigPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ssh.json")
	content := `{"databaseUrl":"postgres://test:secret@localhost/fithub","gitRoot":"/srv/fithub/repos","gitBinary":"/usr/bin/git","sshUser":"git"}`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("private config rejected: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("world-readable SSH gateway config accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "ssh-link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(link); err == nil {
		t.Fatal("symlinked SSH gateway config accepted")
	}
}
