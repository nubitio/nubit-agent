package access

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nubitio/nubit-agent/internal/site"
)

// This file adds SFTP logins beyond the one the site itself owns: the extra
// accounts a customer hands to a developer or an agency, each with its own key
// and its own corner of the site.
//
// They are real unix users rather than extra keys on the site's own account,
// because a login that cannot be told apart from the owner's cannot be scoped
// to a subdirectory or taken away on its own.
//
// Access is granted with POSIX ACLs rather than by group. The document root is
// already owned by the site user with the web server as its group, and both of
// those slots are doing a job: the owner writes, the group serves. An ACL adds
// a third party without displacing either, and the default entries make it
// apply to files the site creates later.

var ftpLabel = regexp.MustCompile(`^[a-z][a-z0-9-]{1,18}$`)

// unixNameLimit is what useradd accepts on Debian and Ubuntu.
const unixNameLimit = 32

// FTPResult describes one additional login.
type FTPResult struct {
	SiteID    string `json:"siteId"`
	Label     string `json:"label"`
	Username  string `json:"username"`
	Directory string `json:"directory"`
	Status    string `json:"status"`
}

// CreateFTPUser adds a login scoped to a directory inside the site.
//
// The directory is relative to the site's document root, and empty means the
// document root itself. It is created if missing, because asking a customer to
// make the folder before they can be given access to it is a step with no
// purpose.
func (manager Manager) CreateFTPUser(siteID, label, publicKey, directory string) (FTPResult, error) {
	state, username, err := manager.ftpUser(siteID, label)
	if err != nil {
		return FTPResult{}, err
	}
	key, err := normalisePublicKey(publicKey)
	if err != nil {
		return FTPResult{}, err
	}
	target, err := scopedDirectory(state, directory)
	if err != nil {
		return FTPResult{}, err
	}

	if contains(state.FTPUsers, label) {
		// A retried command. The key is reapplied rather than refused, so a
		// caller that lost the response can converge without a second label.
		return manager.applyFTPUser(state, label, username, key, target, "active")
	}

	if err := manager.Runner.Run(
		"useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", username,
	); err != nil {
		return FTPResult{}, fmt.Errorf("create the login: %w", err)
	}
	// useradd --system locks the account, and sshd refuses a locked account even
	// for a public key. Deleting the password unlocks it without enabling
	// password authentication, which sshd is configured to refuse anyway.
	if err := manager.Runner.Run("passwd", "-d", username); err != nil {
		_ = manager.Runner.Run("userdel", username)

		return FTPResult{}, fmt.Errorf("unlock the login: %w", err)
	}

	result, err := manager.applyFTPUser(state, label, username, key, target, "active")
	if err != nil {
		_ = manager.Runner.Run("userdel", username)

		return FTPResult{}, err
	}

	return result, nil
}

// UpdateFTPKey replaces the key an existing login authenticates with.
func (manager Manager) UpdateFTPKey(siteID, label, publicKey string) (FTPResult, error) {
	state, username, err := manager.ftpUser(siteID, label)
	if err != nil {
		return FTPResult{}, err
	}
	if !contains(state.FTPUsers, label) {
		return FTPResult{}, errors.New("that login does not exist on this site")
	}
	key, err := normalisePublicKey(publicKey)
	if err != nil {
		return FTPResult{}, err
	}
	directory, err := manager.ftpDirectory(username, state)
	if err != nil {
		return FTPResult{}, err
	}

	return manager.applyFTPUser(state, label, username, key, directory, "active")
}

// DeleteFTPUser removes the login, its key, its access and the unix account.
func (manager Manager) DeleteFTPUser(siteID, label string, confirmed bool) (FTPResult, error) {
	if !confirmed {
		return FTPResult{}, errors.New("removing a login requires explicit confirmation")
	}
	state, username, err := manager.ftpUser(siteID, label)
	if err != nil {
		return FTPResult{}, err
	}
	if !contains(state.FTPUsers, label) {
		return FTPResult{siteID, label, username, "", "absent"}, nil
	}

	keyPath, configPath := manager.paths(username)
	_ = os.Remove(configPath)
	_ = os.Remove(keyPath)
	if err := manager.reload(); err != nil {
		return FTPResult{}, err
	}
	// The ACL goes before the account does: an entry naming a uid that has been
	// freed would grant the next user to be given that number.
	_ = manager.Runner.Run("setfacl", "-R", "-x", "u:"+username, state.DocumentRoot)
	_ = manager.Runner.Run("setfacl", "-R", "-d", "-x", "u:"+username, state.DocumentRoot)
	if err := manager.Runner.Run("userdel", username); err != nil {
		return FTPResult{}, fmt.Errorf("remove the login: %w", err)
	}

	state.FTPUsers = removeValue(state.FTPUsers, label)
	delete(state.FTPDirectories, label)
	if err := manager.Sites.Save(state); err != nil {
		return FTPResult{}, fmt.Errorf("persist site state: %w", err)
	}

	return FTPResult{siteID, label, username, "", "deleted"}, nil
}

// applyFTPUser writes the key, the access and the sshd block, then records it.
func (manager Manager) applyFTPUser(
	state site.State,
	label, username, key, directory, status string,
) (FTPResult, error) {
	if err := manager.Runner.Run(
		"install", "-d", "-o", state.SystemUser, "-g", site.WebServerUser, "-m", "0750", directory,
	); err != nil {
		return FTPResult{}, fmt.Errorf("prepare the directory: %w", err)
	}
	// -R applies to what is already there, -d to what the site writes later.
	// Without the second, files the site creates are unreadable to this login
	// the moment they appear.
	for _, arguments := range [][]string{
		{"setfacl", "-R", "-m", "u:" + username + ":rwX", directory},
		{"setfacl", "-R", "-d", "-m", "u:" + username + ":rwX", directory},
		// The login has to traverse the site root to reach the directory.
		{"setfacl", "-m", "u:" + username + ":--x", filepath.Dir(state.DocumentRoot)},
	} {
		if err := manager.Runner.Run(arguments[0], arguments[1:]...); err != nil {
			return FTPResult{}, fmt.Errorf("grant access to the directory: %w", err)
		}
	}

	if err := os.MkdirAll(manager.KeysDir, 0o755); err != nil {
		return FTPResult{}, err
	}
	if err := os.MkdirAll(manager.ConfigDir, 0o755); err != nil {
		return FTPResult{}, err
	}
	keyPath, configPath := manager.paths(username)
	previousKey, _ := os.ReadFile(keyPath)
	previousConfig, _ := os.ReadFile(configPath)

	if err := writeAtomic(keyPath, []byte(key), 0o600); err != nil {
		return FTPResult{}, err
	}
	// sshd reads AuthorizedKeysFile as the connecting user.
	if err := manager.Runner.Run("chown", username+":"+username, keyPath); err != nil {
		_ = restore(keyPath, previousKey)

		return FTPResult{}, fmt.Errorf("chown authorized keys: %w", err)
	}
	config := fmt.Sprintf(
		"Match User %s\n    ForceCommand internal-sftp -d %s\n    PasswordAuthentication no\n    PubkeyAuthentication yes\n    AuthorizedKeysFile %s\n    DisableForwarding yes\n    PermitTTY no\n",
		username, directory, keyPath,
	)
	if err := writeAtomic(configPath, []byte(config), 0o600); err != nil {
		_ = restore(keyPath, previousKey)

		return FTPResult{}, err
	}
	if err := manager.reload(); err != nil {
		_ = restore(keyPath, previousKey)
		_ = restore(configPath, previousConfig)

		return FTPResult{}, err
	}

	state.FTPUsers = appendUnique(state.FTPUsers, label)
	if state.FTPDirectories == nil {
		state.FTPDirectories = map[string]string{}
	}
	state.FTPDirectories[label] = directory
	if err := manager.Sites.Save(state); err != nil {
		_ = restore(keyPath, previousKey)
		_ = restore(configPath, previousConfig)
		_ = manager.reload()

		return FTPResult{}, fmt.Errorf("persist site state: %w", err)
	}

	return FTPResult{state.SiteID, label, username, directory, status}, nil
}

func (manager Manager) ftpUser(siteID, label string) (site.State, string, error) {
	state, err := manager.site(siteID)
	if err != nil {
		return site.State{}, "", err
	}
	if !ftpLabel.MatchString(label) {
		return site.State{}, "", errors.New("the login name may use lowercase letters, digits and dashes")
	}
	username := state.SystemUser + "-" + label
	if len(username) > unixNameLimit {
		return site.State{}, "", errors.New("the login name is too long for this site")
	}

	return state, username, nil
}

func (manager Manager) ftpDirectory(username string, state site.State) (string, error) {
	_, configPath := manager.paths(username)
	config, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("read the login's configuration: %w", err)
	}
	for _, line := range strings.Split(string(config), "\n") {
		if directory, found := strings.CutPrefix(strings.TrimSpace(line), "ForceCommand internal-sftp -d "); found {
			return directory, nil
		}
	}

	return state.DocumentRoot, nil
}

// scopedDirectory resolves a directory the caller named against the site, and
// refuses anything that leaves it.
//
// A leading slash is read as the site's own root rather than the host's, which
// is what an FTP client shows the customer: they see `/public`, not the path it
// occupies on the machine. `..` is a different matter and is refused, because
// nobody types it meaning "somewhere inside my site".
func scopedDirectory(state site.State, directory string) (string, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" || directory == "." || directory == "/" {
		return state.DocumentRoot, nil
	}
	resolved := filepath.Clean(filepath.Join(state.DocumentRoot, directory))
	// Cleaning collapses `..`, so this compares the result rather than the input
	// and catches every spelling of an escape at once.
	if resolved != state.DocumentRoot && !strings.HasPrefix(resolved, state.DocumentRoot+string(filepath.Separator)) {
		return "", errors.New("the directory must be inside the site")
	}

	return resolved, nil
}

func normalisePublicKey(publicKey string) (string, error) {
	publicKey = strings.TrimSpace(publicKey) + "\n"
	for _, prefix := range []string{"ssh-ed25519 ", "ecdsa-sha2-nistp256 ", "sk-ssh-ed25519@openssh.com "} {
		if strings.HasPrefix(publicKey, prefix) {
			return publicKey, nil
		}
	}

	return "", errors.New("unsupported SSH public key type")
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}

	return false
}

func appendUnique(values []string, value string) []string {
	if contains(values, value) {
		return values
	}

	return append(values, value)
}

func removeValue(values []string, target string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}

	return result
}
