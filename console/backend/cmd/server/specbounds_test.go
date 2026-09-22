package main

import (
	"testing"

	"github.com/slchris/qubes-air/console/internal/config"
	"github.com/slchris/qubes-air/console/internal/service"
	"github.com/stretchr/testify/assert"
)

// TestSpecBoundsFromConfigMatchesServiceDefaults — the wired bounds and the
// service's own fallback must be the same numbers.
//
// They are two copies by construction: config cannot own the service's type
// (only cmd/* links both) and the service cannot read config. If they drift, a
// console with a config file and a console without one enforce different limits,
// and the difference is invisible in both — a bound that is quietly stricter or
// looser than the documented default. This is the pin.
func TestSpecBoundsFromConfigMatchesServiceDefaults(t *testing.T) {
	assert.Equal(t, service.DefaultSpecBounds(), specBoundsFromConfig(config.DefaultConfig().QubeSpec),
		"main.go's mapping and the two default sets must agree")
}
