// Package guestassets holds the guest data checks shared by cloud-init and packaging.
package guestassets

import _ "embed"

//go:embed data-mount.py
var dataMountChecker string

//go:embed mount-plain-data.sh
var plainDataMountScript string

//go:embed qubes-air-data.service
var plainDataMountUnit string

//go:embed 20-data-mount.conf
var plainDataAgentDependency string

// DataMountChecker returns the read-only checker also shipped in the agent package.
func DataMountChecker() string { return dataMountChecker }

// PlainDataMountScript returns the boot-time plain data initializer and mount.
func PlainDataMountScript() string { return plainDataMountScript }

// PlainDataMountUnit returns the fixed boot-time data unit.
func PlainDataMountUnit() string { return plainDataMountUnit }

// PlainDataAgentDependency makes a failed data check block agent startup.
func PlainDataAgentDependency() string { return plainDataAgentDependency }
