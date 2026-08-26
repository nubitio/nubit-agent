package command

import (
	"encoding/json"
	"errors"
	"strings"
)

const SystemPing = "system.ping"
const SiteCreate = "site.create"

type Command struct {
	ID             string          `json:"id"`
	Type           string          `json:"type"`
	Version        int             `json:"version"`
	IdempotencyKey string          `json:"idempotencyKey"`
	Payload        json.RawMessage `json:"payload"`
}

func (command Command) Validate() error {
	if strings.TrimSpace(command.ID) == "" {
		return errors.New("command id is required")
	}
	if strings.TrimSpace(command.Type) == "" {
		return errors.New("command type is required")
	}
	if command.Version != 1 {
		return errors.New("unsupported command version")
	}
	if strings.TrimSpace(command.IdempotencyKey) == "" {
		return errors.New("idempotency key is required")
	}
	if !json.Valid(command.Payload) {
		return errors.New("command payload must be valid JSON")
	}

	return nil
}
