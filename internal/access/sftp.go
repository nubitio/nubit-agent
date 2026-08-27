package access

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nubitio/nubit-agent/internal/site"
)

type Runner interface {
	Run(name string, args ...string) error
}

type Manager struct {
	Runner    Runner
	Sites     site.StateStore
	ConfigDir string
	KeysDir   string
}

type Result struct {
	SiteID     string `json:"siteId"`
	SystemUser string `json:"systemUser"`
	Status     string `json:"status"`
}

func (manager Manager) Create(siteID, publicKey string) (Result, error) {
	return manager.applyKey(siteID, publicKey, true)
}

func (manager Manager) UpdateKey(siteID, publicKey string) (Result, error) {
	return manager.applyKey(siteID, publicKey, false)
}

func (manager Manager) Revoke(siteID string) (Result, error) {
	state, err := manager.site(siteID)
	if err != nil {
		return Result{}, err
	}
	keyPath, configPath := manager.paths(state.SystemUser)
	key, keyErr := os.ReadFile(keyPath)
	config, configErr := os.ReadFile(configPath)
	if errors.Is(keyErr, os.ErrNotExist) && errors.Is(configErr, os.ErrNotExist) {
		return Result{siteID, state.SystemUser, "revoked"}, nil
	}
	if keyErr != nil || configErr != nil {
		return Result{}, errors.New("SFTP configuration is incomplete")
	}
	if err := os.Remove(keyPath); err != nil {
		return Result{}, err
	}
	if err := os.Remove(configPath); err != nil {
		_ = writeAtomic(keyPath, key, 0o600)
		return Result{}, err
	}
	if err := manager.reload(); err != nil {
		_ = writeAtomic(keyPath, key, 0o600)
		_ = writeAtomic(configPath, config, 0o600)
		return Result{}, err
	}
	state.SFTPEnabled = false
	if err := manager.Sites.Save(state); err != nil {
		_ = writeAtomic(keyPath, key, 0o600)
		_ = writeAtomic(configPath, config, 0o600)
		_ = manager.reload()
		return Result{}, err
	}
	return Result{siteID, state.SystemUser, "revoked"}, nil
}

func (manager Manager) applyKey(siteID, publicKey string, requireAbsent bool) (Result, error) {
	state, err := manager.site(siteID)
	if err != nil {
		return Result{}, err
	}
	publicKey = strings.TrimSpace(publicKey) + "\n"
	if !strings.HasPrefix(publicKey, "ssh-ed25519 ") && !strings.HasPrefix(publicKey, "ecdsa-sha2-nistp256 ") && !strings.HasPrefix(publicKey, "sk-ssh-ed25519@openssh.com ") {
		return Result{}, errors.New("unsupported SSH public key type")
	}
	keyPath, configPath := manager.paths(state.SystemUser)
	if requireAbsent {
		if _, err := os.Stat(keyPath); err == nil {
			return Result{}, errors.New("SFTP access already exists")
		}
	}
	if err := os.MkdirAll(manager.KeysDir, 0o755); err != nil {
		return Result{}, err
	}
	_ = os.Chmod(manager.KeysDir, 0o755)
	if err := os.MkdirAll(manager.ConfigDir, 0o755); err != nil {
		return Result{}, err
	}
	staged, err := os.CreateTemp(manager.KeysDir, ".key-")
	if err != nil {
		return Result{}, err
	}
	stagedPath := staged.Name()
	defer os.Remove(stagedPath)
	if err := staged.Chmod(0o600); err == nil {
		_, err = staged.WriteString(publicKey)
	}
	if closeErr := staged.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Result{}, err
	}
	if err := manager.Runner.Run("ssh-keygen", "-l", "-f", stagedPath); err != nil {
		return Result{}, fmt.Errorf("validate SSH public key: %w", err)
	}
	if err := manager.Runner.Run("passwd", "-d", state.SystemUser); err != nil {
		return Result{}, fmt.Errorf("unlock site user for SFTP: %w", err)
	}
	previousKey, _ := os.ReadFile(keyPath)
	previousConfig, _ := os.ReadFile(configPath)
	config := fmt.Sprintf("Match User %s\n    ForceCommand internal-sftp -d %s\n    PasswordAuthentication no\n    PubkeyAuthentication yes\n    AuthorizedKeysFile %s\n    DisableForwarding yes\n    PermitTTY no\n", state.SystemUser, state.DocumentRoot, keyPath)
	if err := writeAtomic(keyPath, []byte(publicKey), 0o600); err != nil {
		return Result{}, err
	}
	// sshd opens AuthorizedKeysFile as the site user (temporarily_use_uid).
	if err := manager.Runner.Run("chown", state.SystemUser+":"+state.SystemUser, keyPath); err != nil {
		_ = restore(keyPath, previousKey)
		return Result{}, fmt.Errorf("chown authorized keys: %w", err)
	}
	if err := writeAtomic(configPath, []byte(config), 0o600); err != nil {
		_ = restore(keyPath, previousKey)
		return Result{}, err
	}
	if err := manager.reload(); err != nil {
		_ = restore(keyPath, previousKey)
		_ = restore(configPath, previousConfig)
		return Result{}, err
	}
	state.SFTPEnabled = true
	if err := manager.Sites.Save(state); err != nil {
		_ = restore(keyPath, previousKey)
		_ = restore(configPath, previousConfig)
		_ = manager.reload()
		return Result{}, err
	}
	return Result{siteID, state.SystemUser, "active"}, nil
}

func (manager Manager) site(siteID string) (site.State, error) {
	if manager.Runner == nil || manager.Sites == nil {
		return site.State{}, errors.New("SFTP manager is not configured")
	}
	state, found := manager.Sites.Get(siteID)
	if !found {
		return site.State{}, errors.New("site not found")
	}
	return state, nil
}

func (manager Manager) paths(user string) (string, string) {
	return filepath.Join(manager.KeysDir, user), filepath.Join(manager.ConfigDir, "nubit-"+user+".conf")
}

func (manager Manager) reload() error {
	if err := manager.Runner.Run("sshd", "-t"); err != nil {
		return fmt.Errorf("validate sshd configuration: %w", err)
	}
	if err := manager.Runner.Run("systemctl", "reload", "ssh"); err != nil {
		return fmt.Errorf("reload ssh: %w", err)
	}
	return nil
}

func writeAtomic(path string, contents []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".nubit-")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(mode); err == nil {
		_, err = temporary.Write(contents)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func restore(path string, contents []byte) error {
	if contents == nil {
		return os.Remove(path)
	}
	return writeAtomic(path, contents, 0o600)
}
