package service

import (
	"fmt"
	"log"
	"strconv"

	"github.com/slchris/qubes-air/console/internal/models"
)

// SpecBounds are the inclusive limits every create/update request's sizes are
// checked against before the request can reach a repository or a provider.
//
// WHY this exists: the check used to reject negative numbers only, so a typo —
// 200000 GiB of disk, 4096 vCPUs — was stored and sent to Proxmox. A PVE disk
// cannot be shrunk (provider/proxmox/adapter.go:170), so the size such a request
// asks for is consumed for as long as that disk exists and no code path gives it
// back. Refusing it at the API boundary is the only place that cost is avoided.
//
// Zero is NOT a size, it means "unset": at create the zero values are replaced
// with the type defaults (applyDefaultSpec) and the adapter falls back to
// defaultDataDiskGB for an unset data disk
// (provider/proxmox/adapter.go:53-55). The minimum therefore applies only to a
// value the caller actually gave, which is what lets the negative-only rule it
// replaces and this one coexist.
//
// The bounds are operator-tunable (`qube_spec.*` / `QUBES_AIR_QUBE_SPEC_*`);
// DefaultSpecBounds is what a console with no configuration enforces.
type SpecBounds struct {
	MinVCPU       int
	MaxVCPU       int
	MinMemoryMB   int
	MaxMemoryMB   int
	MinDiskGB     int
	MaxDiskGB     int
	MinDataDiskGB int
	MaxDataDiskGB int
	MinGPUCount   int
	MaxGPUCount   int
}

// DefaultSpecBounds returns the bounds a console enforces when nothing is
// configured. config.QubeSpecConfig's defaults hold the same numbers and
// TestSpecBoundsFromConfigMatchesServiceDefaults pins the two together, so the
// configured path and this fallback cannot drift apart.
//
// Where the numbers come from — and which ones are judgement calls:
//
//   - VCPU 1..32. Measured: the only UI refuses anything outside 1..32 on both
//     its create and edit forms (console/frontend/src/components/QubeList.svelte
//     :471, :579), so the API accepts exactly what an operator can express
//     today. 32 is also 8x the 4 cores per node of the reference cluster the
//     scheduler was built against (internal/scheduler/scheduler_test.go:12-13),
//     while the built-in type defaults are 2/4/8 (getDefaultVCPU).
//   - Memory 512..262144 MB. The MINIMUM is measured: 512 MB is the UI's own
//     minimum (QubeList.svelte:476) and the size of the console's smallest VM,
//     the data-disk holder (provider/proxmox/adapter.go:306). The MAXIMUM has no
//     basis in this repository — it is a JUDGEMENT CALL, not a measurement.
//     256 GiB is 16x the largest built-in type default (16384 MB,
//     getDefaultMemory) and ~8x the reference node's 31 GiB of RAM
//     (scheduler_test.go:13): above any request this project's deployment model
//     would make, while still refusing a value wrong by orders of magnitude.
//     Move it with qube_spec.max_memory_mb rather than trusting the number.
//   - Disk 10..16384 GB. The MINIMUM is the UI's minimum for the root disk
//     (QubeList.svelte:483); the template could demand more, but that only fails
//     at Proxmox (models/qube.go:207-208). The MAXIMUM is a JUDGEMENT CALL:
//     16 TiB is ~160x the largest built-in root-disk default (100 GB,
//     getDefaultDisk), large enough that no single-operator node reaches it, and
//     it refuses the 200000 GiB typo this bound exists for. The real ceiling is a
//     property of the datastore, which the API cannot see, so this is deliberately
//     a policy bound: qube_spec.max_disk_gb.
//   - DataDiskGB 1..16384 GB. The MINIMUM is the UI's minimum
//     (QubeList.svelte:488). The MAXIMUM shares the root disk's JUDGEMENT CALL
//     and its rationale: this disk holds a qube's persistent data and is the one
//     PVE will never shrink, so it is bounded by the same policy ceiling
//     (qube_spec.max_data_disk_gb).
//   - GPU count 1..8. JUDGEMENT CALL with no basis in the repository: no
//     provider adapter reads Spec.GPU at all today (only negative counts are
//     refused), so there is nothing to measure. 8 keeps a typo (or a GPU type
//     applied to a machine with none) from being recorded as a request for
//     thousands of devices; qube_spec.max_gpu_count is the knob.
func DefaultSpecBounds() SpecBounds {
	return SpecBounds{
		MinVCPU:       1,
		MaxVCPU:       32,
		MinMemoryMB:   512,
		MaxMemoryMB:   262144,
		MinDiskGB:     10,
		MaxDiskGB:     16384,
		MinDataDiskGB: 1,
		MaxDataDiskGB: 16384,
		MinGPUCount:   1,
		MaxGPUCount:   8,
	}
}

// specBound is one bounded dimension: the request field it applies to, the bound
// itself, and the config key an operator would move to change it.
type specBound struct {
	field string // the field as it appears in the API request body
	unit  string // unit suffix, empty for a count
	key   string // key suffix under qube_spec
	min   int
	max   int
}

// bounds lists every size a create/update request can carry. A dimension added
// to models.QubeSpec without being added here would be unbounded, which is the
// gap this type exists to close — keep the two in step.
//
// Node and EncryptData are deliberately absent: neither is a size. Node is a
// placement choice the scheduler and the adapter already reject when it is
// wrong, and EncryptData is a bool.
func (b SpecBounds) bounds() []specBound {
	return []specBound{
		{field: "spec.vcpu", key: "vcpu", min: b.MinVCPU, max: b.MaxVCPU},
		{field: "spec.memory", unit: "MB", key: "memory_mb", min: b.MinMemoryMB, max: b.MaxMemoryMB},
		{field: "spec.disk", unit: "GB", key: "disk_gb", min: b.MinDiskGB, max: b.MaxDiskGB},
		{field: "spec.data_disk_gb", unit: "GB", key: "data_disk_gb", min: b.MinDataDiskGB, max: b.MaxDataDiskGB},
		{field: "spec.gpu.count", key: "gpu_count", min: b.MinGPUCount, max: b.MaxGPUCount},
	}
}

// specValues reads the request's value for each bounded dimension. A dimension
// the request did not carry at all is absent from the map rather than zero, so
// an unset GPU is not checked against a count bound.
func specValues(spec models.QubeSpec) map[string]int {
	values := map[string]int{
		"vcpu":         spec.VCPU,
		"memory_mb":    spec.Memory,
		"disk_gb":      spec.Disk,
		"data_disk_gb": spec.DataDiskGB,
	}
	if spec.GPU != nil {
		values["gpu_count"] = spec.GPU.Count
	}
	return values
}

// validateSpec rejects a spec with a size outside these bounds. The error names
// the field, the value it was given and the limit, plus the config key that
// moves that limit, because "invalid qube spec" alone leaves an operator with
// nothing to act on.
//
// Both bounds are INCLUSIVE: min and max are accepted, min-1 and max+1 are not.
func (b SpecBounds) validateSpec(spec models.QubeSpec) error {
	values := specValues(spec)
	for _, d := range b.bounds() {
		value, present := values[d.key]
		// Zero means unset, not "zero-sized" — see SpecBounds. Only a value that
		// was actually given is held to the minimum.
		if !present || value == 0 {
			continue
		}
		if value < d.min {
			return fmt.Errorf("%w: %s %s is below the minimum %s (qube_spec.min_%s)",
				ErrInvalidQubeSpec, d.field, formatBound(value, d.unit), formatBound(d.min, d.unit), d.key)
		}
		if value > d.max {
			return fmt.Errorf("%w: %s %s is above the maximum %s (qube_spec.max_%s)",
				ErrInvalidQubeSpec, d.field, formatBound(value, d.unit), formatBound(d.max, d.unit), d.key)
		}
	}
	return nil
}

// Validate reports whether these bounds can be enforced at all. It exists so a
// nonsensical set is REFUSED rather than applied: a minimum below 1 would not
// bound anything and a maximum below the minimum would reject every request, and
// both are the kind of typo (a transposed digit) that would otherwise look like a
// working console.
func (b SpecBounds) Validate() error {
	for _, d := range b.bounds() {
		if err := validateBoundPair(d.key, d.min, d.max); err != nil {
			return err
		}
	}
	return nil
}

// validateBoundPair checks one dimension's min/max pair. The config package
// applies the same rule to its own copy of these numbers at startup, so a bad
// bound is refused before it can reach the service.
func validateBoundPair(key string, min, max int) error {
	if min < 1 {
		return fmt.Errorf("qube_spec.min_%s (%d) must be at least 1; a non-positive minimum would not bound anything", key, min)
	}
	if max < min {
		return fmt.Errorf("qube_spec.max_%s (%d) is below qube_spec.min_%s (%d); every request would be refused", key, max, key, min)
	}
	return nil
}

// formatBound renders a bound for an error message, with its unit when it has one.
func formatBound(v int, unit string) string {
	if unit == "" {
		return strconv.Itoa(v)
	}
	return fmt.Sprintf("%d %s", v, unit)
}

// WithSpecBounds replaces the resource bounds every create/update is checked
// against (config: qube_spec.*).
//
// A set that does not pass Validate is IGNORED and the defaults stay in force. A
// bound set is only a limit, so a misconfigured one must never be able to switch
// the check off; config.Config.Validate refuses such a set at startup, and this
// is the same decision made again where the bounds are actually used.
func WithSpecBounds(b SpecBounds) QubeServiceOption {
	return func(s *QubeServiceImpl) {
		if err := b.Validate(); err != nil {
			log.Printf("service: ignoring unusable spec bounds (%v); keeping the defaults", err)
			return
		}
		s.specBounds = b
	}
}
