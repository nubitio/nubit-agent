package mail

import (
	"encoding/json"
	"strings"
	"testing"
)

// fakeJMAP answers the reads from a fixed inventory and records the writes, so
// a test asserts the exact method and payload the manager puts on the wire.
type fakeJMAP struct {
	domains  []domainRecord
	accounts []accountRecord
	calls    []call
	fail     map[string]string
}

type call struct {
	method    string
	arguments map[string]any
}

func (jmap *fakeJMAP) Invoke(method string, arguments map[string]any) (json.RawMessage, error) {
	jmap.calls = append(jmap.calls, call{method: method, arguments: arguments})

	if reason, ok := jmap.fail[method]; ok {
		return json.Marshal(map[string]any{
			"notCreated": map[string]any{"u": map[string]any{"type": "invalidProperties", "description": reason}},
		})
	}

	switch {
	case method == "x:Domain/get":
		return json.Marshal(map[string]any{"list": jmap.domains})
	case method == "x:Account/get":
		return json.Marshal(map[string]any{"list": jmap.accounts})
	case method == "x:Domain/set":
		jmap.domains = append(jmap.domains, domainRecord{ID: "dom1", Name: created(arguments, "name")})
		return json.Marshal(map[string]any{"created": map[string]any{"d": map[string]any{"id": "dom1"}}})
	case method == "x:Account/set":
		if _, creating := arguments["create"]; creating {
			jmap.accounts = append(jmap.accounts, accountRecord{
				ID:           "acc1",
				Name:         created(arguments, "name"),
				DomainID:     created(arguments, "domainId"),
				EmailAddress: created(arguments, "name") + "@example.pe",
			})
			return json.Marshal(map[string]any{"created": map[string]any{"u": map[string]any{"id": "acc1"}}})
		}
		return json.Marshal(map[string]any{"updated": map[string]any{"acc1": nil}})
	}

	return json.Marshal(map[string]any{})
}

// created reads a property out of the single object in a /set create argument.
func created(arguments map[string]any, property string) string {
	create, ok := arguments["create"].(map[string]any)
	if !ok {
		return ""
	}
	for _, object := range create {
		if fields, ok := object.(map[string]any); ok {
			if value, ok := fields[property].(string); ok {
				return value
			}
		}
	}

	return ""
}

func (jmap *fakeJMAP) find(method string) (call, bool) {
	for _, candidate := range jmap.calls {
		if candidate.method == method {
			return candidate, true
		}
	}

	return call{}, false
}

func TestCreateMailboxRegistersTheDomainAndSetsTheSecret(t *testing.T) {
	jmap := &fakeJMAP{}
	manager := Manager{JMAP: jmap}

	result, err := manager.CreateMailbox("Hola@Cliente.PE", "Tr3men.da-Clave_2026!x", 1<<30)
	if err != nil {
		t.Fatal(err)
	}

	if result.Address != "hola@cliente.pe" {
		t.Fatalf("address was not normalised: %q", result.Address)
	}
	if result.Status != "active" || result.ID == "" {
		t.Fatalf("unexpected result: %#v", result)
	}

	// The account is created without an emailAddress on purpose: Stalwart
	// derives it and rejects the property as server-set.
	create, ok := jmap.find("x:Account/set")
	if !ok {
		t.Fatal("no account was created")
	}
	fields := create.arguments["create"].(map[string]any)["u"].(map[string]any)
	if _, present := fields["emailAddress"]; present {
		t.Fatal("emailAddress must not be sent; the server sets it")
	}
	if fields["@type"] != "User" {
		t.Fatalf("wrong account type discriminator: %#v", fields["@type"])
	}
}

func TestCreateMailboxPatchesTheSecretAtTheCredentialIndex(t *testing.T) {
	jmap := &fakeJMAP{}
	manager := Manager{JMAP: jmap}

	if _, err := manager.CreateMailbox("hola@cliente.pe", "Tr3men.da-Clave_2026!x", 0); err != nil {
		t.Fatal(err)
	}

	// Credentials are an indexed list, not a map keyed by name. Sending a map
	// is accepted as valid JSON and then rejected by the server, so the shape
	// is asserted here rather than discovered in production.
	var patched bool
	for _, candidate := range jmap.calls {
		update, ok := candidate.arguments["update"].(map[string]any)
		if !ok {
			continue
		}
		for _, object := range update {
			fields, ok := object.(map[string]any)
			if !ok {
				continue
			}
			secret, ok := fields["credentials/0"].(map[string]any)
			if !ok {
				continue
			}
			patched = true
			if secret["@type"] != "Password" {
				t.Fatalf("wrong credential type: %#v", secret["@type"])
			}
		}
	}
	if !patched {
		t.Fatal("the password was never patched at credentials/0")
	}
}

func TestCreateMailboxIsIdempotent(t *testing.T) {
	jmap := &fakeJMAP{
		domains:  []domainRecord{{ID: "dom1", Name: "cliente.pe"}},
		accounts: []accountRecord{{ID: "acc1", Name: "hola", DomainID: "dom1", EmailAddress: "hola@cliente.pe"}},
	}
	manager := Manager{JMAP: jmap}

	result, err := manager.CreateMailbox("hola@cliente.pe", "Tr3men.da-Clave_2026!x", 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != "acc1" {
		t.Fatalf("a second mailbox was created instead of reusing the existing one: %#v", result)
	}
	for _, candidate := range jmap.calls {
		if _, creating := candidate.arguments["create"]; creating {
			t.Fatalf("%s created an object that already existed", candidate.method)
		}
	}
}

func TestQuotaUsesTheDiskQuotaKey(t *testing.T) {
	jmap := &fakeJMAP{
		domains:  []domainRecord{{ID: "dom1", Name: "cliente.pe"}},
		accounts: []accountRecord{{ID: "acc1", Name: "hola", DomainID: "dom1", EmailAddress: "hola@cliente.pe"}},
	}
	manager := Manager{JMAP: jmap}

	if _, err := manager.SetQuota("hola@cliente.pe", 5<<30); err != nil {
		t.Fatal(err)
	}

	updated, ok := jmap.find("x:Account/set")
	if !ok {
		t.Fatal("the quota was never written")
	}
	fields := updated.arguments["update"].(map[string]any)["acc1"].(map[string]any)
	quotas := fields["quotas"].(map[string]any)
	if _, ok := quotas["maxDiskQuota"]; !ok {
		t.Fatalf("the quota is keyed wrongly: %#v", quotas)
	}
}

func TestDeletingWhatIsAlreadyGoneSucceeds(t *testing.T) {
	manager := Manager{JMAP: &fakeJMAP{}}

	mailbox, err := manager.DeleteMailbox("hola@cliente.pe", true)
	if err != nil {
		t.Fatal(err)
	}
	if mailbox.Status != "absent" {
		t.Fatalf("expected an absent mailbox, got %#v", mailbox)
	}

	domain, err := manager.DeleteDomain("cliente.pe", true)
	if err != nil {
		t.Fatal(err)
	}
	if domain.Status != "absent" {
		t.Fatalf("expected an absent domain, got %#v", domain)
	}
}

func TestDeletionRequiresConfirmation(t *testing.T) {
	manager := Manager{JMAP: &fakeJMAP{}}

	if _, err := manager.DeleteMailbox("hola@cliente.pe", false); err == nil {
		t.Fatal("an unconfirmed mailbox deletion was accepted")
	}
	if _, err := manager.DeleteDomain("cliente.pe", false); err == nil {
		t.Fatal("an unconfirmed domain deletion was accepted")
	}
}

// A /set call reports per-object failures inside a 200 response, so an
// unchecked call reads as success and the mailbox silently has no password.
func TestAPerObjectRejectionIsAnError(t *testing.T) {
	jmap := &fakeJMAP{fail: map[string]string{"x:Domain/set": "Password is too weak"}}
	manager := Manager{JMAP: jmap}

	_, err := manager.CreateDomain("cliente.pe")
	if err == nil {
		t.Fatal("a rejected /set was reported as success")
	}
	if !strings.Contains(err.Error(), "too weak") {
		t.Fatalf("the server's reason was lost: %v", err)
	}
}

func TestInvalidAddressesAreRefused(t *testing.T) {
	manager := Manager{JMAP: &fakeJMAP{}}

	for _, address := range []string{"", "hola", "@cliente.pe", "hola@", "ho la@cliente.pe", "hola@cliente"} {
		if _, err := manager.CreateMailbox(address, "Tr3men.da-Clave_2026!x", 0); err == nil {
			t.Fatalf("%q was accepted as an address", address)
		}
	}
}

func TestNoMailServerConfiguredIsReported(t *testing.T) {
	if _, err := (Manager{}).CreateDomain("cliente.pe"); err == nil {
		t.Fatal("an unconfigured manager reported success")
	}
}
