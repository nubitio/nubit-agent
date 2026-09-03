package inventory

import (
	"bufio"
	"errors"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nubitio/nubit-agent/internal/site"
)

type RuntimeProvider interface {
	RuntimeInventory() ([]site.RuntimeInfo, error)
}

type Snapshot struct {
	CollectedAt  time.Time          `json:"collectedAt"`
	OS           map[string]string  `json:"os"`
	Architecture string             `json:"architecture"`
	MemoryBytes  uint64             `json:"memoryBytes"`
	DiskBytes    uint64             `json:"diskBytes"`
	DiskFree     uint64             `json:"diskFreeBytes"`
	IPAddresses  []string           `json:"ipAddresses"`
	Packages     map[string]string  `json:"packages"`
	Capabilities []string           `json:"capabilities"`
	PHPRuntimes  []site.RuntimeInfo `json:"phpRuntimes"`
}

func Collect(provider RuntimeProvider) (Snapshot, error) {
	phpRuntimes, err := provider.RuntimeInventory()
	if err != nil {
		return Snapshot{}, err
	}
	diskTotal, diskFree, err := diskUsage("/")
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		CollectedAt: time.Now().UTC(), OS: readOSRelease(), Architecture: runtime.GOARCH,
		MemoryBytes: memoryBytes(), DiskBytes: diskTotal, DiskFree: diskFree,
		IPAddresses: ipAddresses(), Packages: packageVersions(),
		Capabilities: []string{"system.reconcile", "system.reset", "site.create", "site.inspect", "site.suspend", "site.resume", "site.delete", "site.add-domain", "site.remove-domain", "site.set-resources", "runtime.set-version", "runtime.inspect", "runtime.remove", "sftp.create", "sftp.update-key", "sftp.revoke", "sftp.user.create", "sftp.user.update-key", "sftp.user.delete", "database.create", "database.rotate-password", "database.delete", "database.user.create", "database.user.delete", "database.grant", "database.revoke", "site.files.list", "site.files.mkdir", "site.files.write", "site.files.read", "site.files.delete", "site.files.unzip", "site.files.rename", "site.usage", "site.logs.read", "site.cron.list", "site.cron.replace", "site.backup.list", "site.backup.create", "site.backup.restore"},
		PHPRuntimes:  phpRuntimes,
	}, nil
}

func readOSRelease() map[string]string {
	result := make(map[string]string)
	file, err := os.Open("/etc/os-release")
	if err != nil {
		return result
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), "=")
		if found && (key == "ID" || key == "VERSION_ID" || key == "PRETTY_NAME") {
			result[key] = strings.Trim(value, `"`)
		}
	}
	return result
}

func memoryBytes() uint64 {
	contents, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kilobytes, _ := strconv.ParseUint(fields[1], 10, 64)
			return kilobytes * 1024
		}
	}
	return 0
}

func diskUsage(path string) (uint64, uint64, error) {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(path, &stats); err != nil {
		return 0, 0, err
	}
	return stats.Blocks * uint64(stats.Bsize), stats.Bavail * uint64(stats.Bsize), nil
}

func ipAddresses() []string {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	result := make([]string, 0, len(addresses))
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err == nil && !ip.IsLoopback() {
			result = append(result, ip.String())
		}
	}
	return result
}

func packageVersions() map[string]string {
	packages := []string{"caddy", "postgresql", "openssh-server", "php8.3-fpm", "php8.4-fpm", "php8.5-fpm"}
	result := make(map[string]string)
	for _, name := range packages {
		output, err := exec.Command("dpkg-query", "-W", "-f=${Version}", name).Output()
		if err == nil {
			result[name] = strings.TrimSpace(string(output))
			continue
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) && !errors.Is(err, exec.ErrNotFound) {
			result[name] = "unknown"
		}
	}
	return result
}
