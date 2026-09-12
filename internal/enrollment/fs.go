package enrollment

import (
	"os"

	"github.com/nubitio/nubit-agent/internal/durable"
)

// writeAtomic writes a synced temporary file, renames it, and syncs the parent
// directory through the shared durable implementation.
func writeAtomic(path string, contents []byte, mode os.FileMode) error {
	return durable.AtomicWrite(path, contents, mode)
}
