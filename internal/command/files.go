package command

import (
	"encoding/json"
	"errors"
)

type SiteFilesPayload struct {
	SiteID        string `json:"siteId"`
	Path          string `json:"path"`
	To            string `json:"to"`
	ContentBase64 string `json:"contentBase64"`
}

func parseSiteFiles(payload json.RawMessage, commandType string) (SiteFilesPayload, error) {
	var request SiteFilesPayload
	if err := json.Unmarshal(payload, &request); err != nil {
		return request, err
	}
	if !domainName.MatchString(request.SiteID) {
		return request, errors.New("site id is invalid")
	}
	switch commandType {
	case SiteFilesWrite:
		if request.Path == "" {
			return request, errors.New("a file name is required")
		}
	case SiteFilesMkdir:
		if request.Path == "" {
			return request, errors.New("a folder name is required")
		}
	case SiteFilesRead, SiteFilesDelete, SiteFilesUnzip:
		if request.Path == "" {
			return request, errors.New("a path is required")
		}
	case SiteFilesRename:
		if request.Path == "" || request.To == "" {
			return request, errors.New("both names are required")
		}
	}
	return request, nil
}
