// Package capacity provides process-local admission control for a shared host.
// It deliberately does not pretend to be a cgroup implementation: cgroups are
// Linux/runtime-specific and are not portable to the agent's supported hosts.
package capacity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

var ErrCapacityExceeded = errors.New("host capacity reservation exceeds the configured envelope")

type Resources struct {
	CPUmilli    int64 `json:"cpuMilli"`
	MemoryBytes int64 `json:"memoryBytes"`
	PHPWorkers  int64 `json:"phpWorkers"`
	PIDs        int64 `json:"pids"`
}

type Config struct {
	Limit                Resources      `json:"limit"`
	ScratchBytes         int64          `json:"scratchBytes"`
	OperationConcurrency map[string]int `json:"operationConcurrency"`
}

type Reservation struct {
	Name      string
	Resources Resources
}

type SiteAdmission struct {
	Name       string
	Generation uint64
	Previous   Resources
	Existed    bool
}

type Snapshot struct {
	Limit          Resources                    `json:"limit"`
	Reserved       Resources                    `json:"reserved"`
	Reservations   int                          `json:"reservations"`
	Operations     map[string]OperationSnapshot `json:"operations"`
	Degraded       bool                         `json:"degraded"`
	DegradedReason string                       `json:"degradedReason,omitempty"`
}

type OperationSnapshot struct {
	Limit int `json:"limit"`
	InUse int `json:"inUse"`
}

type Manager struct {
	mu             sync.Mutex
	config         Config
	reservations   map[string]Resources
	operations     map[string]chan struct{}
	inUse          map[string]int
	degraded       bool
	degradedReason string
	generations    map[string]uint64
}

func New(config Config, existing []Reservation) (*Manager, error) {
	if err := validate(config); err != nil {
		return nil, err
	}
	m := &Manager{config: config, reservations: map[string]Resources{}, operations: map[string]chan struct{}{}, inUse: map[string]int{}, generations: map[string]uint64{}}
	for _, r := range existing {
		m.restoreLocked(r)
	}
	return m, nil
}

func DefaultConfig() Config {
	mem := int64(memoryBytes())
	if mem == 0 {
		mem = 4 << 30
	}
	disk := int64(diskFreeBytes("/"))
	if disk == 0 {
		disk = 10 << 30
	}
	return Config{Limit: Resources{CPUmilli: int64(runtime.NumCPU()) * 800, MemoryBytes: mem * 80 / 100, PHPWorkers: 500, PIDs: 4096}, ScratchBytes: disk / 10, OperationConcurrency: map[string]int{"backup": 1, "restore": 1, "archive": 2, "usage": 1, "logs": 4, "database": 8}}
}

// ConfigFromEnv uses positive values only. Invalid values retain safe defaults.
func ConfigFromEnv() Config {
	c := DefaultConfig()
	set := func(key string, dst *int64) {
		if v, err := strconv.ParseInt(os.Getenv(key), 10, 64); err == nil && v > 0 {
			*dst = v
		}
	}
	set("NUBIT_AGENT_CAPACITY_CPU_MILLI", &c.Limit.CPUmilli)
	set("NUBIT_AGENT_CAPACITY_MEMORY_BYTES", &c.Limit.MemoryBytes)
	set("NUBIT_AGENT_CAPACITY_PHP_WORKERS", &c.Limit.PHPWorkers)
	set("NUBIT_AGENT_CAPACITY_PIDS", &c.Limit.PIDs)
	set("NUBIT_AGENT_CAPACITY_SCRATCH_BYTES", &c.ScratchBytes)
	for name := range c.OperationConcurrency {
		key := "NUBIT_AGENT_OPERATION_CONCURRENCY_" + name
		if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
			c.OperationConcurrency[name] = v
		}
	}
	return c
}

func validate(c Config) error {
	for name, v := range map[string]int64{"cpu": c.Limit.CPUmilli, "memory": c.Limit.MemoryBytes, "workers": c.Limit.PHPWorkers, "pids": c.Limit.PIDs, "scratch": c.ScratchBytes} {
		if v <= 0 {
			return fmt.Errorf("capacity %s must be positive", name)
		}
	}
	for name, n := range c.OperationConcurrency {
		if n <= 0 {
			return fmt.Errorf("operation concurrency %s must be positive", name)
		}
	}
	return nil
}

func (m *Manager) reserveLocked(name string, r Resources) error {
	old := m.reservations[name]
	total := m.totalLocked()
	total = subtract(total, old)
	total = add(total, r)
	if !fits(total, m.config.Limit) {
		return ErrCapacityExceeded
	}
	m.reservations[name] = r
	if fits(total, m.config.Limit) {
		m.degraded = false
		m.degradedReason = ""
	}
	return nil
}
func (m *Manager) ReserveSite(name string, r Resources) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reserveLocked(name, r)
}

func (m *Manager) BeginSite(name string, r Resources) (SiteAdmission, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	previous, existed := m.reservations[name]
	if err := m.reserveLocked(name, r); err != nil {
		return SiteAdmission{}, err
	}
	m.generations[name]++
	return SiteAdmission{Name: name, Generation: m.generations[name], Previous: previous, Existed: existed}, nil
}

// FinishSite only changes state if this admission is still current. A late
// timeout callback therefore cannot restore an older reservation over a newer
// plan update.
func (m *Manager) FinishSite(admission SiteAdmission, success bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.generations[admission.Name] != admission.Generation {
		return
	}
	if !success {
		if admission.Existed {
			m.reservations[admission.Name] = admission.Previous
		} else {
			delete(m.reservations, admission.Name)
		}
	}
	if fits(m.totalLocked(), m.config.Limit) {
		m.degraded = false
		m.degradedReason = ""
	}
}
func (m *Manager) restoreLocked(r Reservation) {
	if err := m.reserveLocked(r.Name, r.Resources); err != nil {
		m.reservations[r.Name] = r.Resources
		m.degraded = true
		m.degradedReason = "existing reservations exceed the measured capacity envelope"
	}
}

// RestoreSite keeps durable state visible even when a smaller measured host
// cannot currently fit it. The resulting degraded flag makes the condition
// observable while new reservations remain subject to normal admission.
func (m *Manager) RestoreSite(r Reservation) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.restoreLocked(r)
}
func (m *Manager) UpdateSite(name string, r Resources) error { return m.ReserveSite(name, r) }
func (m *Manager) ReleaseSite(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.reservations, name)
	m.generations[name]++
	if fits(m.totalLocked(), m.config.Limit) {
		m.degraded = false
		m.degradedReason = ""
	}
}
func (m *Manager) Reservation(name string) (Resources, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.reservations[name]
	return r, ok
}

func (m *Manager) AcquireOperation(ctx context.Context, kind string) (func(), error) {
	m.mu.Lock()
	limit, ok := m.config.OperationConcurrency[kind]
	if !ok {
		m.mu.Unlock()
		return func() {}, nil
	}
	sem, exists := m.operations[kind]
	if !exists {
		sem = make(chan struct{}, limit)
		m.operations[kind] = sem
	}
	m.mu.Unlock()
	select {
	case sem <- struct{}{}:
		m.mu.Lock()
		m.inUse[kind]++
		m.mu.Unlock()
		return func() { <-sem; m.mu.Lock(); m.inUse[kind]--; m.mu.Unlock() }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	ops := map[string]OperationSnapshot{}
	for name, limit := range m.config.OperationConcurrency {
		ops[name] = OperationSnapshot{Limit: limit, InUse: m.inUse[name]}
	}
	return Snapshot{Limit: m.config.Limit, Reserved: m.totalLocked(), Reservations: len(m.reservations), Operations: ops, Degraded: m.degraded, DegradedReason: m.degradedReason}
}

// ScratchAvailable is a fail-safe preflight for operations which can expand a
// full archive. It does not claim ownership of bytes; the operation's own
// timeout and filesystem errors remain authoritative.
func (m *Manager) ScratchAvailable(path string) bool {
	return int64(diskFreeBytes(path)) >= m.config.ScratchBytes
}
func (m *Manager) totalLocked() Resources {
	var out Resources
	for _, r := range m.reservations {
		out = add(out, r)
	}
	return out
}
func add(a, b Resources) Resources {
	return Resources{CPUmilli: a.CPUmilli + b.CPUmilli, MemoryBytes: a.MemoryBytes + b.MemoryBytes, PHPWorkers: a.PHPWorkers + b.PHPWorkers, PIDs: a.PIDs + b.PIDs}
}
func subtract(a, b Resources) Resources {
	return Resources{CPUmilli: a.CPUmilli - b.CPUmilli, MemoryBytes: a.MemoryBytes - b.MemoryBytes, PHPWorkers: a.PHPWorkers - b.PHPWorkers, PIDs: a.PIDs - b.PIDs}
}
func fits(a, b Resources) bool {
	return a.CPUmilli <= b.CPUmilli && a.MemoryBytes <= b.MemoryBytes && a.PHPWorkers <= b.PHPWorkers && a.PIDs <= b.PIDs
}
func memoryBytes() uint64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) > 1 && f[0] == "MemTotal:" {
			n, _ := strconv.ParseUint(f[1], 10, 64)
			return n * 1024
		}
	}
	return 0
}
func diskFreeBytes(path string) uint64 {
	var s syscall.Statfs_t
	if syscall.Statfs(path, &s) != nil {
		return 0
	}
	return s.Bavail * uint64(s.Bsize)
}
