package command

import (
	"encoding/json"
	"errors"
	"regexp"
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
	if err := json.Unmarshal(payload, &site); err != nil { return site, err }
	if !domainName.MatchString(site.Domain) { return site, errors.New("site domain is invalid") }
	if !siteName.MatchString(site.SystemUser) { return site, errors.New("site system user is invalid") }
	if site.PHPVersion != "8.3" { return site, errors.New("unsupported PHP version") }
	return site, nil
}
