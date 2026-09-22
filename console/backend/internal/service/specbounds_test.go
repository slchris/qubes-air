package service

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// specAt returns a spec whose every dimension sits at its minimum — a valid
// baseline — with one dimension overridden by the probe value. The baseline
// matters: a probe that left the other dimensions at zero would be judged on a
// value it did not intend to test, and a failure would point at the wrong bound.
// A key the service does not bound fails the test rather than silently probing
// nothing.
func specAt(t *testing.T, bounds SpecBounds, key string, value int) models.QubeSpec {
	t.Helper()
	spec := models.QubeSpec{
		VCPU:       bounds.MinVCPU,
		Memory:     bounds.MinMemoryMB,
		Disk:       bounds.MinDiskGB,
		DataDiskGB: bounds.MinDataDiskGB,
		GPU:        &models.GPUSpec{Type: "nvidia", Count: bounds.MinGPUCount},
	}
	switch key {
	case "vcpu":
		spec.VCPU = value
	case "memory_mb":
		spec.Memory = value
	case "disk_gb":
		spec.Disk = value
	case "data_disk_gb":
		spec.DataDiskGB = value
	case "gpu_count":
		spec.GPU.Count = value
	default:
		t.Fatalf("specAt: unknown dimension %q", key)
	}
	return spec
}

// boundProbe is one value fed to a dimension and the limit it must be judged
// against: "" means the value must be accepted, "min"/"max" mean it must be
// refused against that limit.
type boundProbe struct {
	name  string
	value int
	limit string
}

// TestSpecBounds_BoundaryTable pins both ends of every bounded dimension:
// min and max are INCLUSIVE, the value on either side is refused with a message
// that names the field, the value, the limit it broke and the config key that
// moves that limit. 0 is not a size — it means "unset" and stays accepted, which
// is the behavior applyDefaultSpec relies on.
func TestSpecBounds_BoundaryTable(t *testing.T) {
	bounds := DefaultSpecBounds()
	require.NoError(t, bounds.Validate(), "the shipped defaults must be enforceable")

	for _, d := range bounds.bounds() {
		t.Run(d.key, func(t *testing.T) {
			probes := []boundProbe{
				{"min (inclusive)", d.min, ""},
				{"max (inclusive)", d.max, ""},
				{"max+1", d.max + 1, "max"},
				{"negative", -1, "min"},
				{"zero means unset", 0, ""},
			}
			// min-1 is 0 when min is 1, and 0 means "unset" rather than a size;
			// the smallest value that dimension must refuse is then -1, which the
			// negative probe above covers.
			if d.min > 1 {
				probes = append(probes, boundProbe{"min-1", d.min - 1, "min"})
			}

			// One subtest per probe: a probe that fails must not stop the next
			// one from being evaluated, or a mutation would hide behind the
			// first assertion that happens to trip.
			for _, p := range probes {
				t.Run(p.name, func(t *testing.T) {
					err := bounds.validateSpec(specAt(t, bounds, d.key, p.value))
					if p.limit == "" {
						require.NoErrorf(t, err, "%s=%d must be accepted", d.key, p.value)
						return
					}

					require.ErrorIsf(t, err, ErrInvalidQubeSpec, "%s=%d must be refused", d.key, p.value)
					msg := err.Error()
					assert.Containsf(t, msg, d.field, "%s: the message must name the field", p.name)
					assert.Containsf(t, msg, strconv.Itoa(p.value), "%s: the message must carry the value", p.name)
					assert.Containsf(t, msg, "qube_spec."+p.limit+"_"+d.key,
						"%s: the message must name the config key that moves the limit", p.name)
					if p.limit == "min" {
						assert.Containsf(t, msg, "below the minimum", p.name)
						assert.Containsf(t, msg, strconv.Itoa(d.min), "%s: the message must carry the limit", p.name)
					} else {
						assert.Containsf(t, msg, "above the maximum", p.name)
						assert.Containsf(t, msg, strconv.Itoa(d.max), "%s: the message must carry the limit", p.name)
					}
				})
			}
		})
	}
}

// TestCreate_RejectsOutOfRangeSpecBeforeTheProvider — an out-of-range request is
// refused at validation time, which is the only point at which the cost G-H10
// describes is avoidable: a PVE disk cannot be shrunk, so a typo that reaches the
// provider is paid for as long as that disk exists. The spy is checked against a
// control create first, so "no calls" is evidence rather than an executor that
// never records anything.
func TestCreate_RejectsOutOfRangeSpecBeforeTheProvider(t *testing.T) {
	fake := orchestrator.NewFakeExecutor()
	zoneSvc, qubeSvc, cleanup := setupQubeServiceWithExecutor(t, fake)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	_, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name: "control-accepted", Type: models.QubeTypeApp, ZoneID: zone.ID,
		Spec: models.QubeSpec{VCPU: 4, Memory: 4096, Disk: 20, DataDiskGB: 10},
	})
	require.NoError(t, err)
	require.Len(t, fake.Calls(), 1,
		"the spy must record the accepted create, otherwise its silence proves nothing")

	for _, tc := range []struct {
		name string
		spec models.QubeSpec
	}{
		{"disk above the maximum", models.QubeSpec{Disk: 200000}}, // G-H10's own example
		{"vcpu above the maximum", models.QubeSpec{VCPU: 4096}},
		{"memory above the maximum", models.QubeSpec{Memory: 999999999}},
		{"data disk above the maximum", models.QubeSpec{DataDiskGB: 200000}},
		{"gpu count above the maximum", models.QubeSpec{GPU: &models.GPUSpec{Type: "nvidia", Count: 64}}},
		{"vcpu below the minimum", models.QubeSpec{VCPU: -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
				Name: "rejected-" + strings.ReplaceAll(tc.name, " ", "-"),
				Type: models.QubeTypeApp, ZoneID: zone.ID, Spec: tc.spec,
			})
			require.ErrorIs(t, err, ErrInvalidQubeSpec)
			assert.Contains(t, err.Error(), "qube_spec.", "the refusal must say which bound it broke")

			assert.Len(t, fake.Calls(), 1, "a rejected spec must never reach the provider")

			qubes, err := qubeSvc.List(ctx, repository.DefaultQubeListOptions())
			require.NoError(t, err)
			assert.Len(t, qubes, 1, "a rejected spec must not be stored")
		})
	}
}

// TestUpdate_RejectsOutOfRangeSpec — Update shares Create's validator, so a qube
// cannot be edited into a spec create would have refused, and the stored row
// keeps its previous values when it is refused.
func TestUpdate_RejectsOutOfRangeSpec(t *testing.T) {
	fake := orchestrator.NewFakeExecutor()
	zoneSvc, qubeSvc, cleanup := setupQubeServiceWithExecutor(t, fake)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	op, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name: "editable", Type: models.QubeTypeApp, ZoneID: zone.ID,
		Spec: models.QubeSpec{VCPU: 2, Memory: 2048, Disk: 20, DataDiskGB: 10},
	})
	require.NoError(t, err)

	tooBig := models.QubeSpec{VCPU: 2, Memory: 2048, Disk: DefaultSpecBounds().MaxDiskGB + 1, DataDiskGB: 10}
	_, err = qubeSvc.Update(ctx, op.Qube.ID, &models.QubeUpdateRequest{Spec: &tooBig})
	require.ErrorIs(t, err, ErrInvalidQubeSpec)
	assert.Contains(t, err.Error(), "spec.disk")
	assert.Contains(t, err.Error(), strconv.Itoa(DefaultSpecBounds().MaxDiskGB),
		"the refusal must name the limit it broke")

	stored, err := qubeSvc.GetByID(ctx, op.Qube.ID)
	require.NoError(t, err)
	assert.Equal(t, 20, stored.Spec.Disk, "a rejected update must not change the row")
}

// TestCreate_ZeroSizesBecomeTheTypeDefaults — 0 is "unset", not "zero-sized":
// create replaces it with the type default, which is why the bounds must not
// refuse it. Without this the minimum bound would break every create that omits a
// size, and the UI form's own values.
func TestCreate_ZeroSizesBecomeTheTypeDefaults(t *testing.T) {
	_, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	op, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name: "defaulted", Type: models.QubeTypeApp,
		Spec: models.QubeSpec{}, // every size unset
	})
	require.NoError(t, err)

	stored, err := qubeSvc.GetByID(ctx, op.Qube.ID)
	require.NoError(t, err)
	assert.Equal(t, getDefaultVCPU(models.QubeTypeApp), stored.Spec.VCPU)
	assert.Equal(t, getDefaultMemory(models.QubeTypeApp), stored.Spec.Memory)
	assert.Equal(t, getDefaultDisk(models.QubeTypeApp), stored.Spec.Disk)
}

// TestCreate_HonorsConfiguredSpecBounds — the bounds are configuration, not
// constants: a console whose deployment wants a lower ceiling must actually
// enforce it.
func TestCreate_HonorsConfiguredSpecBounds(t *testing.T) {
	tight := DefaultSpecBounds()
	tight.MaxVCPU = 8
	tight.MaxDiskGB = 64

	fake := orchestrator.NewFakeExecutor()
	zoneSvc, qubeSvc, cleanup := setupQubeServiceWithExecutor(t, fake, WithSpecBounds(tight))
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	// The configured maximum itself is accepted; one above it is not.
	_, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name: "at-configured-max", Type: models.QubeTypeApp, ZoneID: zone.ID,
		Spec: models.QubeSpec{VCPU: 8, Disk: 64},
	})
	require.NoError(t, err, "the configured maximum must be inclusive")

	_, err = qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name: "above-configured-max", Type: models.QubeTypeApp, ZoneID: zone.ID,
		Spec: models.QubeSpec{VCPU: 9},
	})
	require.ErrorIs(t, err, ErrInvalidQubeSpec)
	assert.Contains(t, err.Error(), "above the maximum 8",
		"the refusal must quote the CONFIGURED limit, not the built-in one")
}

// TestWithSpecBounds_UnusableSetKeepsTheDefaults — a bounds set that cannot bound
// anything must not be able to switch the check off: min < 1 drops the lower
// bound and max < min refuses everything, and both are one transposed digit away.
// The service keeps its defaults instead, so the check is still enforced.
func TestWithSpecBounds_UnusableSetKeepsTheDefaults(t *testing.T) {
	for name, bad := range map[string]SpecBounds{
		"min above max": func() SpecBounds {
			b := DefaultSpecBounds()
			b.MinVCPU, b.MaxVCPU = 64, 32
			return b
		}(),
		"min below one": func() SpecBounds {
			b := DefaultSpecBounds()
			b.MinDiskGB = 0
			return b
		}(),
		"max below one": func() SpecBounds {
			b := DefaultSpecBounds()
			b.MaxGPUCount = -1
			return b
		}(),
		"zero value": {},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, bad.Validate(), "the probe set must be one the service refuses")

			svc := NewQubeService(nil, nil, WithSpecBounds(bad)).(*QubeServiceImpl)
			assert.Equal(t, DefaultSpecBounds(), svc.specBounds,
				"an unusable set must be ignored, not applied")

			err := svc.validateQubeSpec(models.QubeSpec{VCPU: DefaultSpecBounds().MaxVCPU + 1})
			require.ErrorIs(t, err, ErrInvalidQubeSpec,
				"the check must still be enforced after an unusable set is offered")
		})
	}
}
