package sshtransport

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"fithub.local/fithub/internal/store"
	"golang.org/x/crypto/ssh"
)

var ErrInvalidPublicKey = errors.New("invalid SSH public key")

type ParsedKey struct {
	Type        string
	PublicKey   string
	Fingerprint string
}

// ParsePublicKey accepts one ordinary SSH authentication key. OpenSSH key
// options, certificates, multiple keys, and user comments never become part
// of the stored canonical key or authorization command.
func ParsePublicKey(input string) (ParsedKey, error) {
	if len(input) > 8192 || strings.ContainsRune(input, 0) {
		return ParsedKey{}, ErrInvalidPublicKey
	}
	pub, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(input))
	_ = comment // A user's key comment is untrusted metadata and is discarded.
	if err != nil || len(options) != 0 || len(strings.TrimSpace(string(rest))) != 0 {
		return ParsedKey{}, ErrInvalidPublicKey
	}
	if _, isCert := pub.(*ssh.Certificate); isCert {
		return ParsedKey{}, ErrInvalidPublicKey
	}
	switch pub.Type() {
	case ssh.KeyAlgoED25519:
	case ssh.KeyAlgoECDSA256:
		cryptoKey, ok := pub.(ssh.CryptoPublicKey)
		if !ok {
			return ParsedKey{}, ErrInvalidPublicKey
		}
		if key, ok := cryptoKey.CryptoPublicKey().(*ecdsa.PublicKey); !ok || key.Curve.Params().Name != "P-256" {
			return ParsedKey{}, ErrInvalidPublicKey
		}
	case ssh.KeyAlgoRSA:
		cryptoKey, ok := pub.(ssh.CryptoPublicKey)
		if !ok {
			return ParsedKey{}, ErrInvalidPublicKey
		}
		if key, ok := cryptoKey.CryptoPublicKey().(*rsa.PublicKey); !ok || key.N.BitLen() < 3072 {
			return ParsedKey{}, ErrInvalidPublicKey
		}
	default:
		return ParsedKey{}, ErrInvalidPublicKey
	}
	canonical := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	return ParsedKey{Type: pub.Type(), PublicKey: canonical, Fingerprint: ssh.FingerprintSHA256(pub)}, nil
}

func safeCommandPath(path string) bool {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, " \t\r\n\"'\\,=\x00") {
		return false
	}
	return filepath.Clean(path) == path
}

// AuthorizedKeyLine is emitted only by the server-side lookup command.
// restrict disables forwarding, PTY, X11, agent forwarding, and ~/.ssh/rc.
func AuthorizedKeyLine(key store.SSHKey, executable, configPath string) (string, error) {
	if !safeCommandPath(executable) || !safeCommandPath(configPath) || !keyIDRE.MatchString(key.ID) || !strings.HasPrefix(key.Fingerprint, "SHA256:") {
		return "", errors.New("invalid SSH authorized-key configuration")
	}
	parsed, err := ParsePublicKey(key.PublicKey)
	if err != nil || parsed.Fingerprint != key.Fingerprint || parsed.Type != key.KeyType {
		return "", errors.New("stored SSH key is invalid")
	}
	return fmt.Sprintf("command=\"%s serve --config %s --key-id %s\",restrict %s", executable, configPath, key.ID, parsed.PublicKey), nil
}
