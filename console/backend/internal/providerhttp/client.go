// Package providerhttp builds authenticated provider clients with mandatory
// HTTPS identity verification and no redirect-based credential forwarding.
package providerhttp

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// NewClient validates the endpoint before any credentials can be sent. A
// private CA replaces the system roots for this client; endpoint hostname and
// server certificate purpose/validity checks remain mandatory.
func NewClient(endpoint, caPEM string, timeout time.Duration) (*http.Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(endpoint, "\r\n\t ") {
		return nil, errors.New("provider endpoint must be an HTTPS URL without credentials, query or fragment")
	}
	var roots *x509.CertPool
	if caPEM != "" {
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM([]byte(caPEM)) {
			return nil, errors.New("provider CA must contain a PEM certificate")
		}
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return fmt.Errorf("provider redirects are not permitted")
		},
	}, nil
}
