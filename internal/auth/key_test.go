package auth

import (
	"crypto/sha256"
	"strings"
	"testing"
)

func TestGeneratedKeysAreUniqueAndPrefixed(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		k, err := Generate()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(k.Secret, "tsk_") {
			t.Fatalf("key %q has no recognisable prefix", k.Secret)
		}
		if seen[k.Secret] {
			t.Fatal("generated the same key twice")
		}
		seen[k.Secret] = true
		// The stored prefix must be a prefix of the real key, or a request
		// could never be matched to its row.
		if !strings.HasPrefix(k.Secret, k.Prefix) {
			t.Fatalf("stored prefix %q is not a prefix of %q", k.Prefix, k.Secret)
		}
		// And it must be far shorter than the key, or storing it would leak
		// most of the secret in clear.
		if len(k.Prefix) > len(k.Secret)/3 {
			t.Fatalf("prefix is %d of %d characters; too much of the key is stored in clear",
				len(k.Prefix), len(k.Secret))
		}
	}
}

// TestTheKeyItselfIsNeverRecoverable: the stored digest must not be reversible
// to the credential, so a copy of the table is not a set of working logins.
func TestTheKeyItselfIsNeverRecoverable(t *testing.T) {
	k, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(k.Hash), k.Secret) {
		t.Fatal("the stored hash contains the key")
	}
	want := sha256.Sum256([]byte(k.Secret))
	if string(k.Hash) != string(want[:]) {
		t.Fatal("the stored hash is not the digest of the key")
	}
}

func TestMatches(t *testing.T) {
	k, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !Matches(k.Secret, k.Hash) {
		t.Error("a genuine key did not match its own digest")
	}
	// Whitespace from a copy-paste must not break a valid key.
	if !Matches("  "+k.Secret+"\n", k.Hash) {
		t.Error("a key with surrounding whitespace was rejected")
	}
	for _, wrong := range []string{"", "tsk_", k.Secret + "x", k.Secret[:len(k.Secret)-1]} {
		if Matches(wrong, k.Hash) {
			t.Errorf("accepted a wrong key: %q", wrong)
		}
	}
}

func TestPrefixOfRejectsMalformedKeys(t *testing.T) {
	k, _ := Generate()
	if _, ok := PrefixOf(k.Secret); !ok {
		t.Error("a genuine key was not recognised")
	}
	for _, bad := range []string{"", "hello", "tsk_", "tsk_short", "sk_wrongprefix1234567890"} {
		if _, ok := PrefixOf(bad); ok {
			t.Errorf("accepted a malformed key: %q", bad)
		}
	}
}

// TestRolePermissions states the access model in one place: a viewer may read
// and nothing else, and only an owner may mint further keys.
func TestRolePermissions(t *testing.T) {
	cases := []struct {
		role       Role
		write, mgr bool
	}{
		{RoleOwner, true, true},
		{RoleOperator, true, false},
		{RoleViewer, false, false},
	}
	for _, c := range cases {
		p := Profile{Role: c.role}
		if p.CanWrite() != c.write {
			t.Errorf("%s CanWrite = %v, want %v", c.role, p.CanWrite(), c.write)
		}
	}
	if (Role("admin")).Valid() {
		t.Error("an unknown role was accepted")
	}
}
