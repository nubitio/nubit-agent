package mail

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Stalwart derives the address from the local part and the domain, so the local
// part is validated here rather than the address: sending an emailAddress is
// refused by the server as a server-set property.
var (
	domainName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
	localPart  = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$`)
)

// Invoker performs one JMAP method call. Client satisfies it; tests substitute
// their own rather than standing up a mail server.
type Invoker interface {
	Invoke(method string, arguments map[string]any) (json.RawMessage, error)
}

// Manager creates and removes the mail objects a hosting account needs.
//
// Every operation is idempotent in the sense the control plane needs: creating
// a domain or a mailbox that already exists reports the existing one instead of
// failing, so a retried provisioning job converges rather than erroring.
type Manager struct {
	JMAP Invoker
}

// DomainResult reports a mail domain. It deliberately carries no credentials.
type DomainResult struct {
	Domain string `json:"domain"`
	ID     string `json:"id"`
	Status string `json:"status"`
}

// MailboxResult reports a mailbox. The password is never echoed: the control
// plane already knows what it sent, and command output is persisted.
type MailboxResult struct {
	Address   string `json:"address"`
	ID        string `json:"id"`
	Domain    string `json:"domain"`
	QuotaByte int64  `json:"quotaBytes,omitempty"`
	Status    string `json:"status"`
}

type setResponse struct {
	Created map[string]struct {
		ID string `json:"id"`
	} `json:"created"`
	Updated    map[string]json.RawMessage `json:"updated"`
	Destroyed  []string                   `json:"destroyed"`
	NotCreated map[string]methodError     `json:"notCreated"`
	NotUpdated map[string]methodError     `json:"notUpdated"`
	NotDestroy map[string]methodError     `json:"notDestroyed"`
}

type domainRecord struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type accountRecord struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	DomainID     string `json:"domainId"`
	EmailAddress string `json:"emailAddress"`
}

type getResponse[T any] struct {
	List []T `json:"list"`
}

// CreateDomain registers a mail domain, or reports the existing one.
func (manager Manager) CreateDomain(domain string) (DomainResult, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if !domainName.MatchString(domain) {
		return DomainResult{}, fmt.Errorf("mail: %q is not a valid domain", domain)
	}

	existing, err := manager.findDomain(domain)
	if err != nil {
		return DomainResult{}, err
	}
	if existing != "" {
		return DomainResult{Domain: domain, ID: existing, Status: "active"}, nil
	}

	created, err := manager.set("x:Domain/set", map[string]any{
		"create": map[string]any{"d": map[string]any{"name": domain}},
	})
	if err != nil {
		return DomainResult{}, err
	}

	id, err := createdID(created, "d")
	if err != nil {
		return DomainResult{}, err
	}

	return DomainResult{Domain: domain, ID: id, Status: "active"}, nil
}

// DeleteDomain removes a mail domain. Deleting one that is already gone
// succeeds, so a retried teardown converges.
func (manager Manager) DeleteDomain(domain string, confirmed bool) (DomainResult, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if !confirmed {
		return DomainResult{}, errors.New("mail: deleting a domain requires explicit confirmation")
	}

	id, err := manager.findDomain(domain)
	if err != nil {
		return DomainResult{}, err
	}
	if id == "" {
		return DomainResult{Domain: domain, Status: "absent"}, nil
	}

	if _, err := manager.set("x:Domain/set", map[string]any{"destroy": []string{id}}); err != nil {
		return DomainResult{}, err
	}

	return DomainResult{Domain: domain, ID: id, Status: "absent"}, nil
}

// CreateMailbox creates a mailbox under an existing domain and sets its
// password and quota. The domain is created on demand: a hosting account that
// buys mail has no reason to issue two commands for one outcome.
func (manager Manager) CreateMailbox(address, password string, quotaBytes int64) (MailboxResult, error) {
	local, domain, err := splitAddress(address)
	if err != nil {
		return MailboxResult{}, err
	}
	if password == "" {
		return MailboxResult{}, errors.New("mail: a mailbox needs a password")
	}

	domainResult, err := manager.CreateDomain(domain)
	if err != nil {
		return MailboxResult{}, err
	}

	id, err := manager.findAccount(local, domainResult.ID)
	if err != nil {
		return MailboxResult{}, err
	}
	if id == "" {
		created, createErr := manager.set("x:Account/set", map[string]any{
			"create": map[string]any{"u": map[string]any{
				"@type":    "User",
				"name":     local,
				"domainId": domainResult.ID,
			}},
		})
		if createErr != nil {
			return MailboxResult{}, createErr
		}
		if id, err = createdID(created, "u"); err != nil {
			return MailboxResult{}, err
		}
	}

	if err := manager.SetPassword(address, password); err != nil {
		return MailboxResult{}, err
	}
	if quotaBytes > 0 {
		if err := manager.setQuota(id, quotaBytes); err != nil {
			return MailboxResult{}, err
		}
	}

	return MailboxResult{
		Address:   local + "@" + domain,
		ID:        id,
		Domain:    domain,
		QuotaByte: quotaBytes,
		Status:    "active",
	}, nil
}

// SetPassword replaces the mailbox password.
//
// Stalwart holds credentials as an indexed list, so the primary password is
// patched at `credentials/0` rather than assigned as a whole object. It also
// enforces its own strength policy and refuses a weak secret, which surfaces
// here as an error rather than a silently unchanged password.
func (manager Manager) SetPassword(address, password string) error {
	local, domain, err := splitAddress(address)
	if err != nil {
		return err
	}
	if password == "" {
		return errors.New("mail: a mailbox needs a password")
	}

	id, err := manager.mailboxID(local, domain)
	if err != nil {
		return err
	}

	_, err = manager.set("x:Account/set", map[string]any{
		"update": map[string]any{id: map[string]any{
			"credentials/0": map[string]any{"@type": "Password", "secret": password},
		}},
	})

	return err
}

// SetQuota caps how much disk the mailbox may use, in bytes.
func (manager Manager) SetQuota(address string, quotaBytes int64) (MailboxResult, error) {
	local, domain, err := splitAddress(address)
	if err != nil {
		return MailboxResult{}, err
	}

	id, err := manager.mailboxID(local, domain)
	if err != nil {
		return MailboxResult{}, err
	}
	if err := manager.setQuota(id, quotaBytes); err != nil {
		return MailboxResult{}, err
	}

	return MailboxResult{
		Address:   local + "@" + domain,
		ID:        id,
		Domain:    domain,
		QuotaByte: quotaBytes,
		Status:    "active",
	}, nil
}

// DeleteMailbox removes a mailbox and everything stored in it. Deleting one
// that is already gone succeeds.
func (manager Manager) DeleteMailbox(address string, confirmed bool) (MailboxResult, error) {
	local, domain, err := splitAddress(address)
	if err != nil {
		return MailboxResult{}, err
	}
	if !confirmed {
		return MailboxResult{}, errors.New("mail: deleting a mailbox requires explicit confirmation")
	}

	domainID, err := manager.findDomain(domain)
	if err != nil {
		return MailboxResult{}, err
	}
	if domainID == "" {
		return MailboxResult{Address: local + "@" + domain, Domain: domain, Status: "absent"}, nil
	}

	id, err := manager.findAccount(local, domainID)
	if err != nil {
		return MailboxResult{}, err
	}
	if id == "" {
		return MailboxResult{Address: local + "@" + domain, Domain: domain, Status: "absent"}, nil
	}

	if _, err := manager.set("x:Account/set", map[string]any{"destroy": []string{id}}); err != nil {
		return MailboxResult{}, err
	}

	return MailboxResult{Address: local + "@" + domain, ID: id, Domain: domain, Status: "absent"}, nil
}

// Inventory lists every mailbox the server holds, so the control plane can
// reconcile what it believes against what is actually provisioned.
func (manager Manager) Inventory() ([]MailboxResult, error) {
	accounts, err := manager.accounts()
	if err != nil {
		return nil, err
	}

	inventory := make([]MailboxResult, 0, len(accounts))
	for _, account := range accounts {
		address := account.EmailAddress
		domain := ""
		if at := strings.LastIndex(address, "@"); at >= 0 {
			domain = address[at+1:]
		}
		inventory = append(inventory, MailboxResult{
			Address: address,
			ID:      account.ID,
			Domain:  domain,
			Status:  "active",
		})
	}

	return inventory, nil
}

func (manager Manager) setQuota(id string, quotaBytes int64) error {
	if quotaBytes < 0 {
		return errors.New("mail: a quota cannot be negative")
	}

	_, err := manager.set("x:Account/set", map[string]any{
		"update": map[string]any{id: map[string]any{
			"quotas": map[string]any{"maxDiskQuota": quotaBytes},
		}},
	})

	return err
}

func (manager Manager) mailboxID(local, domain string) (string, error) {
	domainID, err := manager.findDomain(domain)
	if err != nil {
		return "", err
	}
	if domainID == "" {
		return "", fmt.Errorf("mail: domain %q is not registered", domain)
	}

	id, err := manager.findAccount(local, domainID)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("mail: mailbox %s@%s does not exist", local, domain)
	}

	return id, nil
}

func (manager Manager) findDomain(domain string) (string, error) {
	arguments, err := manager.invoke("x:Domain/get", map[string]any{"ids": nil})
	if err != nil {
		return "", err
	}

	var response getResponse[domainRecord]
	if err := json.Unmarshal(arguments, &response); err != nil {
		return "", fmt.Errorf("mail: cannot read the domain list: %w", err)
	}
	for _, record := range response.List {
		if strings.EqualFold(record.Name, domain) {
			return record.ID, nil
		}
	}

	return "", nil
}

func (manager Manager) findAccount(local, domainID string) (string, error) {
	accounts, err := manager.accounts()
	if err != nil {
		return "", err
	}
	for _, account := range accounts {
		if account.DomainID == domainID && strings.EqualFold(account.Name, local) {
			return account.ID, nil
		}
	}

	return "", nil
}

func (manager Manager) accounts() ([]accountRecord, error) {
	arguments, err := manager.invoke("x:Account/get", map[string]any{"ids": nil})
	if err != nil {
		return nil, err
	}

	var response getResponse[accountRecord]
	if err := json.Unmarshal(arguments, &response); err != nil {
		return nil, fmt.Errorf("mail: cannot read the account list: %w", err)
	}

	return response.List, nil
}

func (manager Manager) set(method string, arguments map[string]any) (setResponse, error) {
	raw, err := manager.invoke(method, arguments)
	if err != nil {
		return setResponse{}, err
	}

	var response setResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return setResponse{}, fmt.Errorf("mail: cannot read the %s response: %w", method, err)
	}

	// A /set call reports per-object failures in its own payload rather than
	// failing the request, so an unchecked call looks like success.
	for _, failures := range []map[string]methodError{response.NotCreated, response.NotUpdated, response.NotDestroy} {
		for _, failure := range failures {
			return response, fmt.Errorf("mail: %s rejected: %s (%s)", method, failure.Description, failure.Type)
		}
	}

	return response, nil
}

func (manager Manager) invoke(method string, arguments map[string]any) (json.RawMessage, error) {
	if manager.JMAP == nil {
		return nil, errors.New("mail: no mail server is configured")
	}

	return manager.JMAP.Invoke(method, arguments)
}

func createdID(response setResponse, key string) (string, error) {
	created, ok := response.Created[key]
	if !ok || created.ID == "" {
		return "", errors.New("mail: the server reported no id for the object it created")
	}

	return created.ID, nil
}

func splitAddress(address string) (string, string, error) {
	address = strings.ToLower(strings.TrimSpace(address))
	at := strings.LastIndex(address, "@")
	if at <= 0 || at == len(address)-1 {
		return "", "", fmt.Errorf("mail: %q is not a valid address", address)
	}

	local, domain := address[:at], address[at+1:]
	if !localPart.MatchString(local) {
		return "", "", fmt.Errorf("mail: %q is not a valid mailbox name", local)
	}
	if !domainName.MatchString(domain) {
		return "", "", fmt.Errorf("mail: %q is not a valid domain", domain)
	}

	return local, domain, nil
}
