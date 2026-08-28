package command

import (
	"encoding/json"
	"errors"
	"fmt"
)

type TLSLetsEncryptPayload struct {
	SiteID        string   `json:"siteId"`
	Domains       []string `json:"domains"`
	ChallengeType string   `json:"challengeType"`
}

// parseTLSRequest reads either TLS command.
//
// Enabling names the domains the site should be served on; inspecting only
// names the site, because what the certificate covers is a fact to be read off
// it rather than an expectation to be restated.
func parseTLSRequest(commandType string, payload json.RawMessage) (string, string, []string, error) {
	if commandType == TLSCertificateInspect {
		var request struct {
			SiteID string `json:"siteId"`
		}
		if err := json.Unmarshal(payload, &request); err != nil {
			return "", "", nil, err
		}
		if !domainName.MatchString(request.SiteID) {
			return "", "", nil, errors.New("site id is invalid")
		}

		return request.SiteID, "", nil, nil
	}

	request, err := parseTLSLetsEncrypt(payload)
	if err != nil {
		return "", "", nil, err
	}

	return request.SiteID, request.ChallengeType, request.Domains, nil
}

func parseTLSLetsEncrypt(payload json.RawMessage) (TLSLetsEncryptPayload, error) {
	var request TLSLetsEncryptPayload
	if err := json.Unmarshal(payload, &request); err != nil {
		return request, err
	}
	if !domainName.MatchString(request.SiteID) {
		return request, errors.New("site id is invalid")
	}
	if request.ChallengeType != "http-01" {
		return request, fmt.Errorf("tls challenge type %q is not supported: only http-01 is currently implemented. %s will be added when ACME challenge mutation is implemented", request.ChallengeType, request.ChallengeType)
	}
	if len(request.Domains) == 0 {
		return request, errors.New("TLS domains are required")
	}
	for _, domain := range request.Domains {
		if !domainName.MatchString(domain) {
			return request, errors.New("TLS domain is invalid")
		}
	}

	return request, nil
}
