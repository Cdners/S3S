package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/vault"
	"github.com/zalando/go-keyring"
)

func accountSecrets() vault.Credentials {
	return vault.Credentials{AccessKeyID: rand.Text(), SecretAccessKey: rand.Text(), SessionToken: rand.Text()}
}

func TestSaveAccountAllProviders(t *testing.T) {
	keyring.MockInit()
	w := openWorkspaceAt(t.TempDir())
	defer w.close()
	ctx := t.Context()
	for _, provider := range []core.Provider{core.AWS, core.R2, core.OSS, core.TOS, core.S3Compatible} {
		t.Run(string(provider), func(t *testing.T) {
			input := core.Connection{Name: string(provider), Provider: provider}
			switch provider {
			case core.R2:
				input.Endpoint = "https://00000000000000000000000000000000.r2.cloudflarestorage.com"
			case core.OSS:
				input.Region = "cn-hangzhou"
			case core.TOS:
				input.Region = "cn-beijing"
			case core.S3Compatible:
				input.Endpoint = "https://storage.example.test"
			}
			keys := accountSecrets()
			saved := w.SaveAccount(ctx, nil, input, &keys)
			if saved.Error != nil || saved.Data == nil || saved.Data.ID == "" {
				t.Fatalf("save failed: %v", saved.Error)
			}
			stored, err := (vault.Vault{}).Load(*saved.Data)
			if err != nil || stored != keys {
				t.Fatal("credential bundle did not round-trip for its scope")
			}
			wire, err := json.Marshal(saved)
			if err != nil || strings.Contains(string(wire), keys.SecretAccessKey) || strings.Contains(string(wire), keys.AccessKeyID) || strings.Contains(string(wire), keys.SessionToken) {
				t.Fatal("public response leaked credentials")
			}
			if _, err := w.client(ctx, *saved.Data); err != nil {
				t.Fatalf("could not construct scoped adapter: %v", err)
			}
			if removed := w.ForgetCredentials(ctx, *saved.Data); removed.Error != nil || !removed.Data {
				t.Fatal("explicit removal failed")
			}
			if _, err := (vault.Vault{}).Load(*saved.Data); err == nil || err.Error() != "error.credentialsMissing" {
				t.Fatal("removed account remained registered")
			}
		})
	}
}

func TestSaveAccountBlankRetainsScopeAndRejectsStaleEditors(t *testing.T) {
	keyring.MockInit()
	w := openWorkspaceAt(t.TempDir())
	defer w.close()
	ctx := t.Context()
	keys := accountSecrets()
	r := w.SaveAccount(ctx, nil, core.Connection{Name: "original", Provider: core.TOS, Region: "cn-beijing"}, &keys)
	if r.Error != nil || r.Data == nil {
		t.Fatal(r.Error)
	}
	original := *r.Data
	rename := original
	rename.Name = "new display name"
	if result := w.SaveAccount(ctx, &original, rename, &vault.Credentials{}); result.Error != nil {
		t.Fatal(result.Error)
	}
	if bundle, err := (vault.Vault{}).Load(rename); err != nil || bundle != keys {
		t.Fatal("rename with blank inputs discarded the credential")
	}
	otherKeys := accountSecrets()
	if stale := w.SaveAccount(ctx, &original, original, &otherKeys); stale.Data != nil || stale.Error == nil || stale.Error.Code != "error.connectionChanged" {
		t.Fatal("outdated edit accepted")
	}
	retarget := rename
	retarget.Region = "cn-shanghai"
	if result := w.SaveAccount(ctx, &rename, retarget, nil); result.Error != nil {
		t.Fatal(result.Error)
	}
	if _, err := (vault.Vault{}).Load(retarget); err == nil || err.Error() != "error.credentialsMissing" {
		t.Fatal("credential escaped its original region")
	}
	if result := w.SetCredentials(ctx, rename, otherKeys); result.Error == nil {
		t.Fatal("stale credential writer accepted")
	}
	if result := w.SaveAccount(ctx, &retarget, retarget, &otherKeys); result.Error != nil {
		t.Fatal(result.Error)
	}
	if _, err := (vault.Vault{}).Load(rename); err == nil {
		t.Fatal("old scope used new credential")
	}
	if bundle, err := (vault.Vault{}).Load(retarget); err != nil || bundle != otherKeys {
		t.Fatal("new explicit credential unusable")
	}
}

func TestSaveAccountPartialFailureRetryUsesSameID(t *testing.T) {
	keyring.MockInitWithError(errors.New("private keyring detail"))
	w := openWorkspaceAt(t.TempDir())
	defer w.close()
	ctx := t.Context()
	first := core.Connection{Name: "retry", Provider: core.OSS, Region: "cn-hangzhou"}
	keys := accountSecrets()
	partial := w.SaveAccount(ctx, nil, first, &keys)
	if partial.Data == nil || partial.Data.ID == "" || partial.Error == nil || partial.Error.Code != "error.keyringUnavailable" {
		t.Fatal("missing partial-save receipt")
	}
	wire, err := json.Marshal(partial)
	if err != nil || strings.Contains(string(wire), "private keyring detail") || strings.Contains(string(wire), keys.SecretAccessKey) {
		t.Fatal("unsafe partial-save receipt")
	}
	committed := *partial.Data
	if r := w.SaveAccount(ctx, &committed, committed, &vault.Credentials{}); r.Data == nil || r.Error != nil {
		t.Fatal("blank fields required the locked keyring")
	}
	if r := w.ListBuckets(ctx, committed, ""); r.Error == nil || r.Error.Code != "error.keyringUnavailable" {
		t.Fatal("read operation did not report locked credentials")
	}

	keyring.MockInit()
	retry := w.SaveAccount(ctx, &committed, committed, &keys)
	if retry.Error != nil || retry.Data == nil || *retry.Data != committed {
		t.Fatal("retry did not use original id")
	}
	all := w.Connections(ctx)
	if all.Error != nil || len(all.Data) != 1 || all.Data[0] != committed {
		t.Fatal("duplicate connection on retry")
	}
	if bundle, err := (vault.Vault{}).Load(committed); err != nil || bundle != keys {
		t.Fatal("retried keyring write missing")
	}
}

func TestSaveAccountRejectsInvalidInputBeforePersisting(t *testing.T) {
	keyring.MockInit()
	w := openWorkspaceAt(t.TempDir())
	defer w.close()
	ctx := t.Context()
	keys := accountSecrets()
	for _, tc := range []struct {
		profile core.Connection
		input   vault.Credentials
		code    core.ErrorCode
	}{
		{core.Connection{Name: "unknown", Provider: "new"}, keys, core.InvalidConnection},
		{core.Connection{Name: "bad origin", Provider: core.AWS, Endpoint: "https://other.example"}, keys, core.InvalidConnection},
		{core.Connection{Name: "oss region mismatch", Provider: core.OSS, Region: "cn-hangzhou", Endpoint: "https://oss-cn-beijing.aliyuncs.com"}, keys, core.InvalidConnection},
		{core.Connection{Name: "tos foreign origin", Provider: core.TOS, Region: "cn-beijing", Endpoint: "https://foreign.example"}, keys, core.InvalidConnection},
		{core.Connection{Name: "generic insecure", Provider: core.S3Compatible, Endpoint: "http://objects.example.test"}, keys, core.InvalidConnection},
		{core.Connection{Name: "partial", Provider: core.TOS}, vault.Credentials{AccessKeyID: keys.AccessKeyID}, "error.invalidCredentials"},
		{core.Connection{Name: "token only", Provider: core.OSS}, vault.Credentials{SessionToken: keys.SessionToken}, "error.invalidCredentials"},
		{core.Connection{ID: "preexisting", Name: "untrusted id", Provider: core.AWS}, keys, core.InvalidConnection},
	} {
		r := w.SaveAccount(ctx, nil, tc.profile, &tc.input)
		if r.Data != nil || r.Error == nil || r.Error.Code != tc.code {
			t.Fatalf("%s was not rejected before write", tc.profile.Name)
		}
	}
	if all := w.Connections(ctx); all.Error != nil || len(all.Data) != 0 {
		t.Fatal("invalid account persisted")
	}
}
