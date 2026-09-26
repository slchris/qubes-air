package main

import (
	"github.com/slchris/qubes-air/console/internal/handler"
	"github.com/slchris/qubes-air/console/internal/provider"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
	"github.com/slchris/qubes-air/console/internal/systemmetrics"
)

// newMonitoringHandler serves the Console host's own metrics and each running
// qube's live metrics, read from its zone's provider through the same registry
// the executor dispatches through. The host sampler is built once here because
// CPU usage and network rates are deltas against its previous sample.
func newMonitoringHandler(
	zoneRepo repository.ZoneRepository,
	qubeRepo repository.QubeRepository,
	qubeInfraRepo *repository.QubeInfraRepository,
	providerRegistry *provider.Registry,
) *handler.MonitoringHandler {
	return handler.NewMonitoringHandler(systemmetrics.NewHostSampler(),
		service.NewRuntimeMetricsCollector(qubeRepo, zoneRepo, qubeInfraRepo, providerRegistry))
}
