// Package tls reads back the certificate Caddy obtained for a site and reports
// what it is, so the control plane can audit TLS without ever holding a key.
//
// Caddy does the issuing. It already renews on its own schedule and stores the
// result under its data directory, so an agent that tried to run ACME itself
// would be a second issuer racing the first. What the control plane cannot see
// from where it sits is whether a certificate actually exists, who signed it,
// which names it covers and when it expires — and that is exactly what this
// reports.
//
// The private key is never read, never parsed and never leaves the host. Only
// the public certificate is opened, which is the whole reason this can be
// reported upward at all.
package tls

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultStorageDir is where Caddy keeps managed certificates on a Debian or
// Ubuntu host installed from the official package.
const DefaultStorageDir = "/var/lib/caddy/.local/share/caddy/certificates"

// Evidence is what the control plane stores about a certificate. The field
// names are the contract: its sanitizer keeps an allowlist and drops anything
// it does not recognise.
type Evidence struct {
	SiteID      string   `json:"siteId"`
	Domains     []string `json:"domains"`
	Issuer      string   `json:"issuer"`
	Fingerprint string   `json:"fingerprint"`
	NotBefore   string   `json:"notBefore"`
	ExpiresAt   string   `json:"expiresAt"`
	Manager     string   `json:"manager"`
	Status      string   `json:"status"`
	// Stated rather than implied. The control plane records that no key was
	// transferred, and a reader of the stored evidence should not have to infer
	// it from an absence.
	PrivateKeyStored bool `json:"privateKeyStored"`
}

// ErrNoCertificate reports that Caddy has not obtained one yet. It is not a
// failure: a certificate appears only once the domain resolves publicly and the
// challenge completes, which can be minutes or days after the site is created.
var ErrNoCertificate = errors.New("tls: no certificate has been issued for this site yet")

// Inspector reads certificates out of Caddy's storage.
type Inspector struct {
	// StorageDir defaults to DefaultStorageDir when empty.
	StorageDir string
	// Now is injectable so expiry is testable.
	Now func() time.Time
}

func (inspector Inspector) storageDir() string {
	if inspector.StorageDir != "" {
		return inspector.StorageDir
	}

	return DefaultStorageDir
}

func (inspector Inspector) now() time.Time {
	if inspector.Now != nil {
		return inspector.Now()
	}

	return time.Now()
}

// Inspect reports the certificate covering the site's primary domain.
func (inspector Inspector) Inspect(siteID string) (Evidence, error) {
	if strings.TrimSpace(siteID) == "" {
		return Evidence{}, errors.New("tls: a site id is required")
	}

	path, err := inspector.certificatePath(siteID)
	if err != nil {
		return Evidence{}, err
	}

	certificate, err := readCertificate(path)
	if err != nil {
		return Evidence{}, err
	}

	digest := sha256.Sum256(certificate.Raw)
	names := certificateNames(certificate)

	status := "active"
	if inspector.now().After(certificate.NotAfter) {
		// Reported rather than treated as an error: an expired certificate is
		// a real state the control plane has to be able to see and act on.
		status = "expired"
	}

	return Evidence{
		SiteID:           siteID,
		Domains:          names,
		Issuer:           issuerName(certificate),
		Fingerprint:      "sha256:" + hex.EncodeToString(digest[:]),
		NotBefore:        certificate.NotBefore.UTC().Format(time.RFC3339),
		ExpiresAt:        certificate.NotAfter.UTC().Format(time.RFC3339),
		Manager:          "caddy_automatic_tls",
		Status:           status,
		PrivateKeyStored: false,
	}, nil
}

// certificatePath finds the leaf certificate for a domain.
//
// Caddy lays storage out as certificates/<issuer-directory>/<domain>/<domain>.crt,
// and the issuer directory changes with the CA and the ACME endpoint, so the
// tree is walked rather than a path being assumed.
func (inspector Inspector) certificatePath(domain string) (string, error) {
	root := inspector.storageDir()
	wanted := strings.ToLower(domain) + ".crt"

	var found string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// A missing or unreadable subtree is not fatal: the certificate may
			// still be in another issuer's directory.
			return nil //nolint:nilerr // keep walking
		}
		if entry.IsDir() || found != "" {
			return nil
		}
		if strings.ToLower(entry.Name()) == wanted {
			found = path

			return fs.SkipAll
		}

		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("tls: cannot read %s: %w", root, err)
	}
	if found == "" {
		return "", ErrNoCertificate
	}

	return found, nil
}

func readCertificate(path string) (*x509.Certificate, error) {
	// Bounded: a certificate is a few kilobytes, and this path is chosen by
	// walking a directory rather than by the control plane.
	raw, err := os.ReadFile(path) //nolint:gosec // path comes from Caddy's own storage
	if err != nil {
		return nil, fmt.Errorf("tls: cannot read the certificate: %w", err)
	}

	for block, rest := pem.Decode(raw); block != nil; block, rest = pem.Decode(rest) {
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("tls: the certificate could not be parsed: %w", err)
		}

		// The first certificate in the chain is the leaf, which is the one that
		// carries the names and the expiry the control plane audits.
		return certificate, nil
	}

	return nil, errors.New("tls: the file holds no certificate")
}

func certificateNames(certificate *x509.Certificate) []string {
	seen := make(map[string]struct{}, len(certificate.DNSNames)+1)
	names := make([]string, 0, len(certificate.DNSNames)+1)
	for _, name := range append([]string{certificate.Subject.CommonName}, certificate.DNSNames...) {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)

	return names
}

func issuerName(certificate *x509.Certificate) string {
	if certificate.Issuer.CommonName != "" {
		return certificate.Issuer.CommonName
	}
	if len(certificate.Issuer.Organization) > 0 {
		return certificate.Issuer.Organization[0]
	}

	return certificate.Issuer.String()
}
