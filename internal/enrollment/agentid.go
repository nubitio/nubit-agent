package enrollment

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// agentIDFile is the basename of the file that stores the persistent agent
// identity. It lives under the agent state directory (/var/lib/nubit-agent by
// default), not under the config directory, so reinstalls that wipe /etc keep
// the same identity — and so the mTLS cert stays bound to the same node across
// key rotations of the mTLS material itself.
const agentIDFile = "agent.id"

// LoadOrCreateAgentID returns a stable per-node identifier, creating and
// persisting one on first call. The identifier is 16 random bytes encoded as
// 32 lowercase hex characters; it is opaque to Nubit Control and never
// leaves the host except inside certificate fields (CSR CN, URI SAN).
func LoadOrCreateAgentID(stateDirectory string) (string, error) {
	if stateDirectory == "" {
		return "", errors.New("state directory is required")
	}
	if err := os.MkdirAll(stateDirectory, 0o750); err != nil {
		return "", fmt.Errorf("prepare state directory: %w", err)
	}
	path := filepath.Join(stateDirectory, agentIDFile)
	contents, err := os.ReadFile(path)
	if err == nil {
		id := string(bytesTrimSpace(contents))
		if isValidAgentID(id) {
			return id, nil
		}
		return "", fmt.Errorf("agent id file %s is corrupt", path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read agent id: %w", err)
	}
	id, err := generateAgentID()
	if err != nil {
		return "", err
	}
	if err := writeAtomic(path, []byte(id+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("persist agent id: %w", err)
	}
	return id, nil
}

func generateAgentID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate agent id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func isValidAgentID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

func bytesTrimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end {
		if b[start] != ' ' && b[start] != '\n' && b[start] != '\r' && b[start] != '\t' {
			break
		}
		start++
	}
	for end > start {
		if b[end-1] != ' ' && b[end-1] != '\n' && b[end-1] != '\r' && b[end-1] != '\t' {
			break
		}
		end--
	}
	return b[start:end]
}
