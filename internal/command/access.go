package command

import (
	"encoding/json"
	"errors"
)

type SFTPPayload struct {
	SiteID    string `json:"siteId"`
	PublicKey string `json:"publicKey"`
}

func parseSFTP(payload json.RawMessage, requireKey bool) (SFTPPayload, error) {
	var request SFTPPayload
	if err := json.Unmarshal(payload, &request); err != nil {
		return request, err
	}
	if !domainName.MatchString(request.SiteID) {
		return request, errors.New("site id is invalid")
	}
	if requireKey && request.PublicKey == "" {
		return request, errors.New("SSH public key is required")
	}
	return request, nil
}

// SFTPUserPayload addresses one additional login on a site.
type SFTPUserPayload struct {
	SiteID    string `json:"siteId"`
	Label     string `json:"label"`
	PublicKey string `json:"publicKey"`
	Directory string `json:"directory"`
	Confirm   bool   `json:"confirm"`
}

func parseSFTPUser(payload json.RawMessage, requireKey, requireConfirmation bool) (SFTPUserPayload, error) {
	var request SFTPUserPayload
	if err := json.Unmarshal(payload, &request); err != nil {
		return request, err
	}
	if !domainName.MatchString(request.SiteID) || request.Label == "" {
		return request, errors.New("SFTP user payload is invalid")
	}
	if requireKey && request.PublicKey == "" {
		return request, errors.New("an SSH public key is required")
	}
	if requireConfirmation && !request.Confirm {
		return request, errors.New("removing a login requires explicit confirmation")
	}

	return request, nil
}
