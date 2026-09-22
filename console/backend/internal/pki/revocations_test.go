package pki

import (
	"crypto/x509"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSignedRevocations(t *testing.T) {
	ca, err := NewCA("test", time.Hour)
	require.NoError(t, err)
	wrong, err := NewCA("wrong", time.Hour)
	require.NoError(t, err)
	now := time.Now()
	fp := strings.Repeat("ab", 32)
	doc, err := ca.SignRevocations([]string{fp}, now)
	require.NoError(t, err)
	state, err := VerifyRevocations(doc, []*x509.Certificate{ca.Cert}, now)
	require.NoError(t, err)
	require.Equal(t, []string{fp}, state.Revoked)
	_, err = VerifyRevocations(doc, []*x509.Certificate{wrong.Cert}, now)
	require.Error(t, err)
	_, err = VerifyRevocations(doc, []*x509.Certificate{ca.Cert}, now.Add(RevocationLifetime))
	require.Error(t, err)
	_, err = VerifyRevocations(doc, []*x509.Certificate{ca.Cert}, now.Add(-time.Minute))
	require.Error(t, err)
	var signed signedRevocations
	require.NoError(t, json.Unmarshal(doc, &signed))
	signed.Payload = []byte(`{"version":1,"revoked":[]}`)
	tampered, err := json.Marshal(signed)
	require.NoError(t, err)
	_, err = VerifyRevocations(tampered, []*x509.Certificate{ca.Cert}, now)
	require.Error(t, err)
	_, err = ca.SignRevocations([]string{"invalid"}, now)
	require.Error(t, err)
	for _, u := range []string{"", "file:///tmp/a", "http://u:p@localhost", "http://localhost\nBAD=1", "http://localhost?x=1"} {
		require.Error(t, ValidateRevocationURL(u))
	}
}
