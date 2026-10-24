package service

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func installerTLSFixture(t *testing.T, handler http.HandlerFunc) (*httptest.Server, installerTools) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	ca := filepath.Join(t.TempDir(), "download-ca.pem")
	require.NoError(t, os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{
		Type: certificatePEMType, Bytes: server.Certificate().Raw,
	}), 0o600))
	return server, installerTools{realCurl: true, caBundle: ca}
}

func TestInstallerDownloadsVerifiedHTTPSRedirect(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		name := "verified package"
		if mismatch {
			name = "redirected package digest mismatch"
		}
		t.Run(name, func(t *testing.T) {
			var downloaded atomic.Int32
			server, tools := installerTLSFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/release" {
					http.Redirect(w, r, "/asset.deb", http.StatusFound)
					return
				}
				downloaded.Add(1)
				_, _ = w.Write(testPkgPayload)
			})
			pkg := testAgentPackage()
			pkg.URL = server.URL + "/release"
			if mismatch {
				pkg.SHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			}
			out, _ := renderWith(t, pkg)
			run := runInstallerWithTools(t, out, "0", "", tools)
			require.Equal(t, int32(1), downloaded.Load(), run.output)
			if mismatch {
				assert.NotEqual(t, 0, run.code)
				assert.False(t, run.installed(), run.calls)
				assert.Contains(t, run.marker, "SHA256 mismatch")
			} else {
				assert.Equal(t, 0, run.code, run.output)
				assert.True(t, run.installed(), run.calls)
				assert.Empty(t, run.marker)
			}
			assert.False(t, run.downloadRemains)
		})
	}
}

func TestInstallerRefusesHTTPSDowngrade(t *testing.T) {
	var downloaded atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downloaded.Add(1)
		_, _ = w.Write(testPkgPayload)
	}))
	defer plain.Close()
	server, tools := installerTLSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/asset.deb", http.StatusFound)
	})
	pkg := testAgentPackage()
	pkg.URL = server.URL + "/release"
	out, _ := renderWith(t, pkg)
	run := runInstallerWithTools(t, out, "0", "", tools)
	assert.NotEqual(t, 0, run.code)
	assert.Zero(t, downloaded.Load(), "HTTP target must never receive the redirected request")
	assert.False(t, run.installed(), run.calls)
	assert.Contains(t, run.marker, "download failed")
	assert.False(t, run.downloadRemains)
}

func TestInstallerBoundsHTTPSRedirectLoop(t *testing.T) {
	var requests atomic.Int32
	server, tools := installerTLSFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	pkg := testAgentPackage()
	pkg.URL = server.URL + "/loop"
	out, _ := renderWith(t, pkg)
	run := runInstallerWithTools(t, out, "0", "", tools)
	assert.NotEqual(t, 0, run.code)
	assert.Equal(t, int32(6), requests.Load(), "initial request plus five redirects")
	assert.False(t, run.installed(), run.calls)
	assert.Contains(t, run.marker, "download failed")
	assert.False(t, run.downloadRemains)
}

func TestInstallerResolvesPackageDependencies(t *testing.T) {
	out, _ := renderFixture(t)
	run := runInstallerWithTools(t, out, "0", "", installerTools{missingDependencies: "1"})
	assert.Equal(t, 0, run.code, run.output)
	assert.Contains(t, run.calls, "apt-get install --yes --no-install-recommends ")
	assert.NotContains(t, run.calls, "dpkg -i")
	assert.Contains(t, run.calls, "systemctl enable --now qubes-air-agent")
	assert.Empty(t, run.marker)
	assert.False(t, run.downloadRemains)
}

func TestInstallerDependencyFailureStopsBeforeStart(t *testing.T) {
	out, _ := renderFixture(t)
	run := runInstallerWithTools(t, out, "0", "", installerTools{missingDependencies: "1", aptExit: "100"})
	assert.NotEqual(t, 0, run.code)
	assert.Contains(t, run.marker, "package installation failed")
	assert.NotContains(t, run.calls, "systemctl enable --now qubes-air-agent")
	assert.False(t, run.downloadRemains)
}

func TestInstallerRequiresDeclaredCurlDependency(t *testing.T) {
	out, _ := renderFixture(t)
	run := runInstallerWithTools(t, out, "0", "", installerTools{withoutCurl: true})
	assert.NotEqual(t, 0, run.code)
	assert.Contains(t, run.output, "curl is unavailable")
	assert.Contains(t, run.marker, "download failed")
	assert.NotContains(t, run.calls, "wget ")
	assert.False(t, run.installed(), run.calls)
	assert.False(t, run.downloadRemains)
}

func TestInstallerRejectsUntrustedHTTPSCertificate(t *testing.T) {
	var requests atomic.Int32
	server, tools := installerTLSFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(testPkgPayload)
	})
	_, wrongCA := testIdentityDoc(t)
	require.NoError(t, os.WriteFile(tools.caBundle, []byte(wrongCA), 0o600))
	pkg := testAgentPackage()
	pkg.URL = server.URL + "/asset.deb"
	out, _ := renderWith(t, pkg)
	run := runInstallerWithTools(t, out, "0", "", tools)
	assert.NotEqual(t, 0, run.code)
	assert.Zero(t, requests.Load(), "untrusted TLS server must not receive an HTTP request")
	assert.Contains(t, run.marker, "download failed")
	assert.False(t, run.installed(), run.calls)
	assert.False(t, run.downloadRemains)
}
