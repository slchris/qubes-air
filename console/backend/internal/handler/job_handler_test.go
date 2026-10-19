package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/slchris/qubes-air/console/internal/orchestrator"
)

func getJobLog(t *testing.T, r *gin.Engine, id string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+id+"/log", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body
}

// An empty pane has four different causes. The handler must say which one,
// because "expired" tells an operator to look elsewhere while "lost" tells
// them the log directory did not come back with the database.
func TestJobLogReportsWhyItIsUnavailable(t *testing.T) {
	r, jobs, logs := streamRig(t)
	ctx := context.Background()
	now := time.Now().UTC()

	insertJob(t, jobs, "none", orchestrator.JobSucceeded)
	insertJob(t, jobs, "running", orchestrator.JobRunning)

	insertJob(t, jobs, "available", orchestrator.JobSucceeded)
	f, err := logs.Create("available")
	require.NoError(t, err)
	_, err = f.WriteString("provisioning disk\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	insertJob(t, jobs, "expired", orchestrator.JobSucceeded)
	require.NoError(t, jobs.MarkLogPruned(ctx, "expired", now))

	insertJob(t, jobs, "lost", orchestrator.JobSucceeded)
	require.NoError(t, jobs.MarkLogCreated(ctx, "lost", now))

	cases := []struct {
		id        string
		state     string
		noteWant  string
		noteEmpty bool
		data      string
	}{
		{id: "available", state: jobLogAvailable, noteEmpty: true, data: "provisioning disk"},
		{id: "none", state: jobLogNone, noteWant: "no log was recorded"},
		// A running job that has not printed yet is normal, not a fault.
		{id: "running", state: jobLogNone, noteEmpty: true},
		{id: "expired", state: jobLogExpired, noteWant: "expired"},
		{id: "lost", state: jobLogLost, noteWant: "restored"},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			body := getJobLog(t, r, tc.id)
			assert.Equal(t, tc.state, body["log_state"])
			if tc.data != "" {
				assert.Contains(t, body["data"], tc.data)
			}
			note, _ := body["note"].(string)
			if tc.noteEmpty {
				assert.Empty(t, note, "no note expected for a normal state")
			} else {
				assert.Contains(t, note, tc.noteWant)
			}
		})
	}
}

// A console without job logging must say so rather than serving an empty 200
// that looks like a job which printed nothing.
func TestJobLogDisabledIsExplicit(t *testing.T) {
	_, jobs, _ := streamRig(t)
	insertJob(t, jobs, "job-nolog", orchestrator.JobSucceeded)

	gin.SetMode(gin.TestMode)
	plain := gin.New()
	NewJobHandler(jobs, nil).RegisterRoutes(plain.Group("/api/v1"))

	body := getJobLog(t, plain, "job-nolog")
	assert.Equal(t, jobLogDisabled, body["log_state"])
	assert.Contains(t, body["note"], "not enabled")
}

func TestJobLogUnknownJobIs404(t *testing.T) {
	r, _, _ := streamRig(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/nope/log", nil))
	assert.Equal(t, http.StatusNotFound, w.Code)
}
