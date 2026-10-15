package command

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/nubitio/nubit-agent/internal/mail"
)

type fakeMail struct {
	MailProvisioner
	address string
	aliases []string
	err     error
}

func (fake *fakeMail) SetAliases(address string, aliases []string) (mail.MailboxResult, error) {
	fake.address, fake.aliases = address, aliases

	return mail.MailboxResult{Address: address, Aliases: aliases, Status: "active"}, fake.err
}

func TestMailAliasSetPassesTheCompleteListToTheMailServer(t *testing.T) {
	fake := &fakeMail{}
	executor := NewExecutor(NewMemoryStore(), fake)

	result, err := executor.Execute(Command{
		ID: "cmd_alias", Type: MailAliasSet, Version: 1, IdempotencyKey: "mail:alias:set",
		Payload: []byte(`{"address":" Hola@Cliente.PE ","aliases":["info@cliente.pe","ventas@cliente.pe"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.address != "hola@cliente.pe" || len(fake.aliases) != 2 {
		t.Fatalf("unexpected call: %q %#v", fake.address, fake.aliases)
	}
	var out mail.MailboxResult
	if err := json.Unmarshal(result.Output, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Aliases) != 2 {
		t.Fatalf("the result must report the aliases that are now in force: %#v", out)
	}
}

func TestMailAliasSetAcceptsAnEmptyListToClear(t *testing.T) {
	fake := &fakeMail{}
	executor := NewExecutor(NewMemoryStore(), fake)

	if _, err := executor.Execute(Command{
		ID: "cmd_alias_clear", Type: MailAliasSet, Version: 1, IdempotencyKey: "mail:alias:clear",
		Payload: []byte(`{"address":"hola@cliente.pe","aliases":[]}`),
	}); err != nil {
		t.Fatal(err)
	}
	if fake.aliases == nil || len(fake.aliases) != 0 {
		t.Fatalf("an explicit empty list must reach the mail server as empty: %#v", fake.aliases)
	}
}

func TestMailAliasSetRefusesAMissingList(t *testing.T) {
	fake := &fakeMail{}
	executor := NewExecutor(NewMemoryStore(), fake)

	// A payload that forgot the field must not be read as "remove every alias".
	_, err := executor.Execute(Command{
		ID: "cmd_alias_missing", Type: MailAliasSet, Version: 1, IdempotencyKey: "mail:alias:missing",
		Payload: []byte(`{"address":"hola@cliente.pe"}`),
	})
	if err == nil {
		t.Fatal("a payload without aliases was accepted")
	}
	if fake.address != "" {
		t.Fatal("the mail server was called for an invalid payload")
	}
}

func TestMailAliasSetNeedsAnAddress(t *testing.T) {
	executor := NewExecutor(NewMemoryStore(), &fakeMail{})

	if _, err := executor.Execute(Command{
		ID: "cmd_alias_noaddr", Type: MailAliasSet, Version: 1, IdempotencyKey: "mail:alias:noaddr",
		Payload: []byte(`{"aliases":[]}`),
	}); err == nil {
		t.Fatal("a payload without a mailbox was accepted")
	}
}

func TestMailAliasSetReportsAnAddressInUse(t *testing.T) {
	executor := NewExecutor(NewMemoryStore(), &fakeMail{err: mail.ErrAddressInUse})

	_, err := executor.Execute(Command{
		ID: "cmd_alias_taken", Type: MailAliasSet, Version: 1, IdempotencyKey: "mail:alias:taken",
		Payload: []byte(`{"address":"hola@cliente.pe","aliases":["info@cliente.pe"]}`),
	})
	if !errors.Is(err, mail.ErrAddressInUse) {
		t.Fatalf("the typed error must survive the executor, got %v", err)
	}
}

func TestMailAliasSetWithoutAMailServerIsRefused(t *testing.T) {
	executor := NewExecutor(NewMemoryStore())

	if _, err := executor.Execute(Command{
		ID: "cmd_alias_nomail", Type: MailAliasSet, Version: 1, IdempotencyKey: "mail:alias:nomail",
		Payload: []byte(`{"address":"hola@cliente.pe","aliases":[]}`),
	}); err == nil {
		t.Fatal("an alias command ran on a node without mail")
	}
}
