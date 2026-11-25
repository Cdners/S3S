package vault

import (
	"errors"
	"testing"

	"github.com/Cdners/S3S/internal/core"
	"github.com/zalando/go-keyring"
)

// Use the OS adapter's in-memory backend to exercise serialization and scope
// checks. Real platform keyring availability is a separate integration check.
func TestCredentialScopeAndReplacement(t *testing.T) {
	keyring.MockInit()
	v := Vault{}
	a := core.Connection{ID: "id", Name: "old", Provider: core.AWS, Region: "us-east-1"}
	dummy := Credentials{AccessKeyID: t.Name(), SecretAccessKey: t.Name()}
	if err := v.Save(a, dummy); err != nil {
		t.Fatal(err)
	}
	a.Name = "renamed"
	if value, err := v.Load(a); value != dummy || err != nil {
		t.Fatal("rename broke credential reference")
	}
	for _, field := range []string{"endpoint", "provider", "region", "id"} {
		b := a
		switch field {
		case "endpoint":
			b.Endpoint = "https://example.test"
		case "provider":
			b.Provider = core.R2
		case "region":
			b.Region = "us-west-2"
		case "id":
			b.ID = "other"
		}
		if value, err := v.Load(b); err == nil || value != (Credentials{}) {
			t.Fatalf("%s exposed original credentials", field)
		}
	}
	b := a
	b.Endpoint = "https://example.test"
	if err := v.Save(b, dummy); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Load(a); err == nil {
		t.Fatal("replacement exposed keys to old endpoint")
	}
	if err := v.Forget(b); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Load(b); err == nil {
		t.Fatal("deleted credentials still accessible")
	}
}

func TestUnavailableKeyringReturnsOnlySafeCode(t *testing.T) {
	keyring.MockInitWithError(errors.New("private keyring detail"))
	_, err := (Vault{}).Load(core.Connection{ID: "test"})
	if err == nil || err.Error() != "error.keyringUnavailable" {
		t.Fatalf("unexpected returned error")
	}
}
