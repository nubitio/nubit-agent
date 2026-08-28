package command

import (
	"encoding/json"
	"errors"
)

type DatabasePayload struct {
	SiteID   string `json:"siteId"`
	Database string `json:"database"`
	Role     string `json:"role"`
	Password string `json:"password"`
	Confirm  bool   `json:"confirm"`
}

func parseDatabase(payload json.RawMessage, requirePassword, requireConfirmation bool) (DatabasePayload, error) {
	var request DatabasePayload
	if err := json.Unmarshal(payload, &request); err != nil {
		return request, err
	}
	if !domainName.MatchString(request.SiteID) || request.Database == "" || request.Role == "" {
		return request, errors.New("database payload is invalid")
	}
	if requirePassword && request.Password == "" {
		return request, errors.New("database password is required")
	}
	if requireConfirmation && !request.Confirm {
		return request, errors.New("database deletion requires explicit confirmation")
	}
	return request, nil
}

// parseDatabaseUser reads a command about a user rather than about a database.
// The database name is optional here: creating and deleting a user names none,
// while granting and revoking name the one being opened.
func parseDatabaseUser(payload json.RawMessage, requirePassword, requireDatabase, requireConfirmation bool) (DatabasePayload, error) {
	var request DatabasePayload
	if err := json.Unmarshal(payload, &request); err != nil {
		return request, err
	}
	if !domainName.MatchString(request.SiteID) || request.Role == "" {
		return request, errors.New("database user payload is invalid")
	}
	if requireDatabase && request.Database == "" {
		return request, errors.New("database user payload needs a database")
	}
	if requirePassword && request.Password == "" {
		return request, errors.New("database password is required")
	}
	if requireConfirmation && !request.Confirm {
		return request, errors.New("deleting a database user requires explicit confirmation")
	}

	return request, nil
}
