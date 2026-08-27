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
