package command

import (
	"encoding/json"
	"errors"
	"strings"
)

// MailPayload addresses one mail domain or one mailbox.
//
// A mailbox is named by its address rather than by a server-side id: the
// control plane owns the commercial record and knows the address it sold, and
// making it track opaque ids as well would give the two sides a second thing to
// keep in agreement.
type MailPayload struct {
	Domain     string `json:"domain"`
	Address    string `json:"address"`
	Password   string `json:"password"`
	QuotaBytes int64  `json:"quotaBytes"`
	Confirm    bool   `json:"confirm"`
}

func parseMail(payload json.RawMessage, commandType string) (MailPayload, error) {
	var request MailPayload
	if err := json.Unmarshal(payload, &request); err != nil {
		return request, err
	}

	request.Domain = strings.ToLower(strings.TrimSpace(request.Domain))
	request.Address = strings.ToLower(strings.TrimSpace(request.Address))

	switch commandType {
	case MailDomainCreate, MailDomainDelete:
		if request.Domain == "" {
			return request, errors.New("mail payload needs a domain")
		}
	default:
		if request.Address == "" {
			return request, errors.New("mail payload needs an address")
		}
	}

	switch commandType {
	case MailMailboxCreate, MailMailboxSetPassword:
		if request.Password == "" {
			return request, errors.New("mail payload needs a password")
		}
	case MailDomainDelete, MailMailboxDelete:
		if !request.Confirm {
			return request, errors.New("deleting mail requires explicit confirmation")
		}
	}

	if request.QuotaBytes < 0 {
		return request, errors.New("a mail quota cannot be negative")
	}

	return request, nil
}
