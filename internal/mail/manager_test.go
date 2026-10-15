package mail

import (
	"encoding/json"
	"errors"
	"reflect"
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
	// rejectUpdate, when set, makes an Account update fail with that server error type.
	rejectUpdate string
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
		if jmap.rejectUpdate != "" {
			return json.Marshal(map[string]any{
				"notUpdated": map[string]any{"acc1": map[string]any{"type": jmap.rejectUpdate, "description": "rejected by the server"}},
			})
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

func aliasFixture() *fakeJMAP {
	return &fakeJMAP{
		domains:  []domainRecord{{ID: "dom1", Name: "cliente.pe"}, {ID: "dom2", Name: "otro.pe"}},
		accounts: []accountRecord{{ID: "acc1", Name: "hola", DomainID: "dom1", EmailAddress: "hola@cliente.pe"}},
	}
}

func aliasUpdate(t *testing.T, jmap *fakeJMAP) map[string]any {
	t.Helper()
	for _, candidate := range jmap.calls {
		update, ok := candidate.arguments["update"].(map[string]any)
		if !ok {
			continue
		}
		if fields, ok := update["acc1"].(map[string]any); ok {
			if aliases, ok := fields["aliases"].(map[string]any); ok {
				return aliases
			}
		}
	}
	t.Fatal("no alias update was sent")

	return nil
}

func TestSetAliasesWritesTheWholeIndexedList(t *testing.T) {
	jmap := aliasFixture()

	result, err := Manager{JMAP: jmap}.SetAliases("Hola@Cliente.PE", []string{"Info@Cliente.PE", "ventas@otro.pe"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Aliases, []string{"info@cliente.pe", "ventas@otro.pe"}) {
		t.Fatalf("aliases were not normalised: %#v", result.Aliases)
	}

	// The server's list is indexed ("0", "1"...) and re-indexes on removal, so the
	// full list goes out under positional keys, never a patch of one position.
	sent := aliasUpdate(t, jmap)
	want := map[string]any{
		"0": map[string]any{"name": "info", "domainId": "dom1", "enabled": true},
		"1": map[string]any{"name": "ventas", "domainId": "dom2", "enabled": true},
	}
	if !reflect.DeepEqual(sent, want) {
		t.Fatalf("unexpected alias list on the wire: %#v", sent)
	}
}

func TestSetAliasesWithAnEmptyListClearsThem(t *testing.T) {
	jmap := aliasFixture()

	if _, err := (Manager{JMAP: jmap}).SetAliases("hola@cliente.pe", []string{}); err != nil {
		t.Fatal(err)
	}
	if sent := aliasUpdate(t, jmap); len(sent) != 0 {
		t.Fatalf("an empty list must send an empty set, got %#v", sent)
	}
}

func TestSetAliasesIsIdempotent(t *testing.T) {
	first, second := aliasFixture(), aliasFixture()
	list := []string{"info@cliente.pe"}

	if _, err := (Manager{JMAP: first}).SetAliases("hola@cliente.pe", list); err != nil {
		t.Fatal(err)
	}
	if _, err := (Manager{JMAP: second}).SetAliases("hola@cliente.pe", list); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(aliasUpdate(t, first), aliasUpdate(t, second)) {
		t.Fatal("the same input must produce the same write")
	}
}

func TestSetAliasesRefusesWhatCouldNeverWork(t *testing.T) {
	cases := map[string][]string{
		"its own address":        {"hola@cliente.pe"},
		"a duplicate":            {"info@cliente.pe", "INFO@cliente.pe"},
		"an unregistered domain": {"info@desconocido.pe"},
		"a malformed address":    {"no-es-una-direccion"},
	}
	for name, aliases := range cases {
		jmap := aliasFixture()
		if _, err := (Manager{JMAP: jmap}).SetAliases("hola@cliente.pe", aliases); err == nil {
			t.Errorf("%s was accepted", name)
		}
		for _, candidate := range jmap.calls {
			if candidate.method == "x:Account/set" {
				t.Errorf("%s still reached the server", name)
			}
		}
	}

	tooMany := make([]string, MaxAliases+1)
	for i := range tooMany {
		tooMany[i] = strings.Repeat("a", i+1) + "@cliente.pe"
	}
	if _, err := (Manager{JMAP: aliasFixture()}).SetAliases("hola@cliente.pe", tooMany); err == nil {
		t.Error("more than MaxAliases was accepted")
	}
}

func TestSetAliasesNeedsAnExistingMailbox(t *testing.T) {
	if _, err := (Manager{JMAP: aliasFixture()}).SetAliases("nadie@cliente.pe", []string{"info@cliente.pe"}); err == nil {
		t.Fatal("an alias was set on a mailbox that does not exist")
	}
}

func TestAnAddressAlreadyInUseIsATypedError(t *testing.T) {
	jmap := aliasFixture()
	jmap.rejectUpdate = "primaryKeyViolation"

	_, err := Manager{JMAP: jmap}.SetAliases("hola@cliente.pe", []string{"info@cliente.pe"})
	if !errors.Is(err, ErrAddressInUse) {
		t.Fatalf("expected ErrAddressInUse, got %v", err)
	}

	jmap.rejectUpdate = "invalidForeignKey"
	if _, err = (Manager{JMAP: jmap}).SetAliases("hola@cliente.pe", []string{"info@cliente.pe"}); err == nil || errors.Is(err, ErrAddressInUse) {
		t.Fatalf("another server rejection must stay a plain error, got %v", err)
	}
}

func TestInventoryListsAliasesAsAddresses(t *testing.T) {
	jmap := aliasFixture()
	jmap.accounts[0].Aliases = map[string]aliasRecord{
		"0": {Name: "Ventas", DomainID: "dom2", Enabled: true},
		"1": {Name: "info", DomainID: "dom1", Enabled: true},
		"2": {Name: "apagado", DomainID: "dom1", Enabled: false},
	}

	inventory, err := Manager{JMAP: jmap}.Inventory()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inventory[0].Aliases, []string{"info@cliente.pe", "ventas@otro.pe"}) {
		t.Fatalf("aliases must be sorted addresses without disabled ones: %#v", inventory[0].Aliases)
	}
}

func TestInventoryWithoutAliasesCostsNoDomainLookup(t *testing.T) {
	jmap := aliasFixture()

	if _, err := (Manager{JMAP: jmap}).Inventory(); err != nil {
		t.Fatal(err)
	}
	if _, called := jmap.find("x:Domain/get"); called {
		t.Fatal("the domain list was read although no mailbox has aliases")
	}
}
