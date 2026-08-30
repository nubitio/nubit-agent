package enrollment

import (
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
)

// BuildCSR returns the DER and PEM encodings of a Certificate Signing Request
// for this agent. The CN embeds the per-node identity and the URI SAN uses the
// "agent://<id>" form so Nubit Control can bind the issued certificate to the
// same identity it sees in subsequent signed requests. The DNS SAN carries the
// node hostname so the cert is also usable against a control-plane endpoint
// that performs a hostname check rather than a URI check.
func BuildCSR(signer crypto.Signer, agentID, hostname string) (csrDER []byte, csrPEM []byte, err error) {
	if signer == nil {
		return nil, nil, errors.New("signer is required")
	}
	if !isValidAgentID(agentID) {
		return nil, nil, fmt.Errorf("agent id %q is malformed", agentID)
	}
	if hostname == "" {
		hostname = "localhost"
	}
	identityURI, err := url.Parse("agent://" + agentID)
	if err != nil {
		return nil, nil, fmt.Errorf("build identity uri: %w", err)
	}
	template := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: "agent:" + agentID},
		DNSNames: []string{hostname},
		URIs:     []*url.URL{identityURI},
	}
	csrDER, err = x509.CreateCertificateRequest(rand.Reader, template, signer)
	if err != nil {
		return nil, nil, fmt.Errorf("create csr: %w", err)
	}
	csrPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	return csrDER, csrPEM, nil
}
