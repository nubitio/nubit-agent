package site

import "testing"

func TestUnsetLimitsFallBackToTheSharedTier(t *testing.T) {
	resources := Resources{}.WithDefaults()
	if resources != DefaultResources() {
		t.Fatalf("an unset plan did not get the shared tier: %#v", resources)
	}
	// Raising one limit must not silently reset the other, which is what a
	// control plane sending only a worker count would otherwise do.
	half := Resources{Workers: 12}.WithDefaults()
	if half.Workers != 12 || half.MemoryLimitMB != DefaultResources().MemoryLimitMB {
		t.Fatalf("setting one limit dropped the other: %#v", half)
	}
}

// A node cannot let a figure from the control plane be large enough to take the
// host down, however that figure came to be sent.
func TestLimitsOutsideTheBoundsAreRefused(t *testing.T) {
	for name, resources := range map[string]Resources{
		"no workers":       {Workers: 0, MemoryLimitMB: 128},
		"too many workers": {Workers: maxWorkers + 1, MemoryLimitMB: 128},
		"negative workers": {Workers: -1, MemoryLimitMB: 128},
		"memory too small": {Workers: 5, MemoryLimitMB: minMemoryLimitMB - 1},
		"memory too large": {Workers: 5, MemoryLimitMB: maxMemoryLimitMB + 1},
	} {
		if err := resources.Validate(); err == nil {
			t.Fatalf("%s was accepted: %#v", name, resources)
		}
	}
	if err := DefaultResources().Validate(); err != nil {
		t.Fatalf("the default tier does not validate: %v", err)
	}
}
