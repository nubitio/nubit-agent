package site

import "fmt"

// Resources is what a plan buys a site: how many requests it may serve at once
// and how much memory each of those may use.
//
// Their product is the site's real ceiling. Every pool of a PHP version is a
// child of one systemd unit, so a cgroup cannot be aimed at an individual site
// and there is nowhere else for the limit to live.
type Resources struct {
	Workers       int `json:"workers"`
	MemoryLimitMB int `json:"memoryLimitMb"`
}

// DefaultResources is the shared-hosting tier: 5 x 128 MiB is 640 MiB per site
// in the worst case, which is what a 4 GiB node is sized against.
func DefaultResources() Resources {
	return Resources{Workers: 5, MemoryLimitMB: 128}
}

// Bounds are deliberately narrow. The control plane decides what a plan is
// worth, but a node cannot let one site's figure be large enough to take the
// host down with it.
const (
	minWorkers       = 1
	maxWorkers       = 50
	minMemoryLimitMB = 64
	maxMemoryLimitMB = 2048
)

// WithDefaults fills in whichever half the caller left unset. A plan that only
// raises memory should not silently lose its worker count, and vice versa.
func (resources Resources) WithDefaults() Resources {
	defaults := DefaultResources()
	if resources.Workers == 0 {
		resources.Workers = defaults.Workers
	}
	if resources.MemoryLimitMB == 0 {
		resources.MemoryLimitMB = defaults.MemoryLimitMB
	}

	return resources
}

// Validate refuses a figure outside the bounds rather than quietly clamping it:
// a site running at a limit nobody asked for is harder to explain than a
// command that failed.
func (resources Resources) Validate() error {
	if resources.Workers < minWorkers || resources.Workers > maxWorkers {
		return fmt.Errorf("workers must be between %d and %d, got %d", minWorkers, maxWorkers, resources.Workers)
	}
	if resources.MemoryLimitMB < minMemoryLimitMB || resources.MemoryLimitMB > maxMemoryLimitMB {
		return fmt.Errorf(
			"the memory limit must be between %d and %d MiB, got %d",
			minMemoryLimitMB, maxMemoryLimitMB, resources.MemoryLimitMB,
		)
	}

	return nil
}
