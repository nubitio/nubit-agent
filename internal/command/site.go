package command

import (
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/nubitio/nubit-agent/internal/php"
)

var siteName = regexp.MustCompile(`^[a-z][a-z0-9-]{2,62}$`)
var domainName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

type SiteCreatePayload struct {
	Domain     string `json:"domain"`
	SystemUser string `json:"systemUser"`
	PHPVersion string `json:"phpVersion"`
}

func parseSiteCreate(payload json.RawMessage) (SiteCreatePayload, error) {
	var site SiteCreatePayload
	if err := json.Unmarshal(payload, &site); err != nil {
		return site, err
	}
	if !domainName.MatchString(site.Domain) {
		return site, errors.New("site domain is invalid")
	}
	if !siteName.MatchString(site.SystemUser) {
		return site, errors.New("site system user is invalid")
	}
	if err := php.ValidateNewSite(site.PHPVersion, time.Now().UTC()); err != nil {
		return site, err
	}
	return site, nil
}

type SiteInspectPayload struct {
	SiteID string `json:"siteId"`
}

type PHPSetVersionPayload struct {
	SiteID     string `json:"siteId"`
	PHPVersion string `json:"phpVersion"`
}

type PHPRuntimePayload struct {
	PHPVersion string `json:"phpVersion"`
	Confirm    bool   `json:"confirm"`
}

type SiteDomainPayload struct {
	SiteID string `json:"siteId"`
	Domain string `json:"domain"`
}

type SiteDeletePayload struct {
	SiteID  string `json:"siteId"`
	Confirm bool   `json:"confirm"`
}

func parseSiteInspect(payload json.RawMessage) (SiteInspectPayload, error) {
	var request SiteInspectPayload
	if err := json.Unmarshal(payload, &request); err != nil {
		return request, err
	}
	if !domainName.MatchString(request.SiteID) {
		return request, errors.New("site id is invalid")
	}
	return request, nil
}

func parsePHPSetVersion(payload json.RawMessage) (PHPSetVersionPayload, error) {
	var request PHPSetVersionPayload
	if err := json.Unmarshal(payload, &request); err != nil {
		return request, err
	}
	if !domainName.MatchString(request.SiteID) {
		return request, errors.New("site id is invalid")
	}
	if err := php.ValidateNewSite(request.PHPVersion, time.Now().UTC()); err != nil {
		return request, err
	}
	return request, nil
}

func parsePHPRuntime(payload json.RawMessage) (PHPRuntimePayload, error) {
	var request PHPRuntimePayload
	if err := json.Unmarshal(payload, &request); err != nil {
		return request, err
	}
	if _, found := php.Lookup(request.PHPVersion, time.Now().UTC()); !found {
		return request, errors.New("unknown PHP version")
	}
	if !request.Confirm {
		return request, errors.New("runtime removal requires explicit confirmation")
	}
	return request, nil
}

func parseSiteDomain(payload json.RawMessage) (SiteDomainPayload, error) {
	var request SiteDomainPayload
	if err := json.Unmarshal(payload, &request); err != nil {
		return request, err
	}
	if !domainName.MatchString(request.SiteID) || !domainName.MatchString(request.Domain) {
		return request, errors.New("site domain is invalid")
	}
	return request, nil
}

func parseSiteDelete(payload json.RawMessage) (SiteDeletePayload, error) {
	var request SiteDeletePayload
	if err := json.Unmarshal(payload, &request); err != nil {
		return request, err
	}
	if !domainName.MatchString(request.SiteID) {
		return request, errors.New("site id is invalid")
	}
	if !request.Confirm {
		return request, errors.New("site deletion requires explicit confirmation")
	}
	return request, nil
}
