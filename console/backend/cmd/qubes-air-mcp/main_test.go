package main

import "testing"

// The loopback default is a security control, so it is tested in both
// directions: every input that must be refused, and every input that must be
// allowed. --allow-non-loopback is the only bypass, and it must still insist on
// https: over plain http the bearer token crosses the network in cleartext.
func TestValidateAPIURL(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		allow   bool
		wantErr bool
	}{
		{"loopback ipv4", "http://127.0.0.1:8080", false, false},
		{"loopback localhost", "http://localhost:8080", false, false},
		{"loopback ipv6", "http://[::1]:8080", false, false},
		{"loopback 127/8 range", "http://127.5.5.5:8080", false, false},
		{"loopback with path", "http://127.0.0.1:8080/api/v1", false, false},
		{"remote host refused by default", "https://console.example.com", false, true},
		{"remote host refused by default over http", "http://console.example.com:8080", false, true},
		{"remote host allowed with explicit opt-in over https", "https://console.example.com", true, false},
		{"remote host still refused over http even with opt-in", "http://console.example.com", true, true},
		{"bare host:port", "console.example.com:8080", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAPIURL(tc.raw, tc.allow)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("validateAPIURL(%q, allow=%v) error = %v, wantErr = %v", tc.raw, tc.allow, err, tc.wantErr)
			}
		})
	}
}
