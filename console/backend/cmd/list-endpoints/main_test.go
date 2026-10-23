package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/require"
)

func endpointDB(t *testing.T, count int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "console.db")
	cfg := database.DefaultConfig()
	cfg.DSN = path
	db, err := database.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ctx := context.Background()
	require.NoError(t, repository.NewZoneRepository(db).Create(ctx, &models.Zone{
		ID: "z", Name: "zone", Type: models.ZoneTypeProxmox, Status: models.ZoneStatusConnected,
	}))
	repo := repository.NewQubeRepository(db)
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("qube-%03d", i)
		require.NoError(t, repo.Create(ctx, &models.Qube{
			ID: name, Name: name, Type: models.QubeTypeApp, ZoneID: "z",
			Status: models.QubeStatusRunning, IPAddress: "192.0.2.1",
		}))
	}
	return path
}

func endpointCommand(t *testing.T, path string) (string, error) {
	t.Helper()
	oldFlags, oldArgs, oldOut := flag.CommandLine, os.Args, os.Stdout
	defer func() { flag.CommandLine, os.Args, os.Stdout = oldFlags, oldArgs, oldOut }()
	flag.CommandLine = flag.NewFlagSet("list-endpoints", flag.ContinueOnError)
	os.Args = []string{"list-endpoints", "-db", path}
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	require.NoError(t, err)
	os.Stdout = out
	runErr := run()
	require.NoError(t, out.Close())
	b, err := os.ReadFile(out.Name())
	require.NoError(t, err)
	return string(b), runErr
}

func TestSnapshotIncludesRunningQubesBeyondFirstPage(t *testing.T) {
	path := endpointDB(t, 105)
	out, err := endpointCommand(t, path)
	require.NoError(t, err)
	for i := 0; i < 105; i++ {
		require.Contains(t, out, fmt.Sprintf("qube-%03d 192.0.2.1:8443\n", i))
	}
	lines := strings.SplitAfter(out, "\n")
	require.Equal(t, "QUBES-AIR-ENDPOINTS 1\n", lines[0])
	records := strings.Join(lines[1:106], "")
	require.Equal(t, fmt.Sprintf("END 105 %x\n", sha256.Sum256([]byte(records))), lines[106])
}

func TestSnapshotRejectsInvalidDataWithoutPartialOutput(t *testing.T) {
	path := endpointDB(t, 2)
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer db.Close()
	for _, address := range []string{"192.0.2.1\nextra", "999.0.0.1", "::1"} {
		_, err = db.Exec("UPDATE qubes SET ip_address = ? WHERE name = 'qube-000'", address)
		require.NoError(t, err)
		out, err := endpointCommand(t, path)
		require.Error(t, err)
		require.Empty(t, out)
	}
	_, err = db.Exec("UPDATE qubes SET ip_address = '', status = 'purged'")
	require.NoError(t, err)
	out, err := endpointCommand(t, path)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("QUBES-AIR-ENDPOINTS 1\nEND 0 %x\n", sha256.Sum256(nil)), out)
}

func TestSnapshotRefusesOverflowAndBadPorts(t *testing.T) {
	path := endpointDB(t, maxEndpoints+1)
	out, err := endpointCommand(t, path)
	require.ErrorContains(t, err, "exceeds")
	require.Empty(t, out)
	db, err := database.OpenReadOnly(context.Background(), path)
	require.NoError(t, err)
	defer db.Close()
	for _, port := range []string{"0", "65536", "08443", "not-a-port", "8443\n"} {
		_, err := endpointSnapshot(context.Background(), db, port)
		require.Error(t, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = endpointSnapshot(ctx, db, "8443")
	require.ErrorIs(t, err, context.Canceled)
}

func TestSnapshotMissingDatabaseCannotBecomeAnEmptyFleet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	out, err := endpointCommand(t, path)
	require.Error(t, err)
	require.Empty(t, out)
	require.NoFileExists(t, path)
}
