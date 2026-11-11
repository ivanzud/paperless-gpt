package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureJobLog redirects the job logger into a buffer at the given level.
func captureJobLog(t *testing.T, level logrus.Level) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevLevel := logger.Out, logger.GetLevel()
	logger.SetOutput(&buf)
	logger.SetLevel(level)
	t.Cleanup(func() {
		logger.SetOutput(prevOut)
		logger.SetLevel(prevLevel)
	})
	return &buf
}

func TestJobLogsDoNotContainDocumentText(t *testing.T) {
	const (
		existing = "EXISTING-CONTENT Jane Doe 1234 Oak Ave"
		result   = "OCR-RESULT Example Bank statement for John Doe"
	)
	store := &JobStore{jobs: make(map[string]*Job)}
	job := &Job{
		ID:         "job-1",
		DocumentID: 42,
		Status:     "pending",
		TotalPages: 3,
		Options:    OCROptions{ExistingContent: existing},
	}

	buf := captureJobLog(t, logrus.InfoLevel)
	store.addJob(job)
	store.updatePagesDone(job.ID, 2)
	store.updateJobStatus(job.ID, "completed", result)

	out := buf.String()
	assert.NotContains(t, out, "OCR-RESULT")
	assert.NotContains(t, out, "EXISTING-CONTENT")
	assert.NotContains(t, out, "Jane Doe")
	assert.NotContains(t, out, "John Doe")

	assert.Contains(t, out, "Job added")
	assert.Contains(t, out, "Job pages done updated")
	assert.Contains(t, out, "Job status updated")
	assert.Contains(t, out, "job_id=job-1")
	assert.Contains(t, out, "document_id=42")
	assert.Contains(t, out, "status=completed")
	assert.Contains(t, out, "pages_done=2")
	assert.Contains(t, out, "total_pages=3")
	assert.Contains(t, out, "result_length=46")
	require.Len(t, result, 46)
}

func TestJobResultIsLoggedAtDebugOnly(t *testing.T) {
	store := &JobStore{jobs: make(map[string]*Job)}
	job := &Job{ID: "job-2", DocumentID: 7, Status: "pending"}

	buf := captureJobLog(t, logrus.DebugLevel)
	store.addJob(job)
	store.updateJobStatus(job.ID, "completed", "OCR-RESULT text")

	assert.Contains(t, buf.String(), "OCR-RESULT text")
}

func TestSkipAccessLog(t *testing.T) {
	gin.SetMode(gin.TestMode)

	run := func(t *testing.T, level logrus.Level, method, path string, status int) string {
		t.Helper()
		prevLevel := log.GetLevel()
		log.SetLevel(level)
		t.Cleanup(func() { log.SetLevel(prevLevel) })

		var access bytes.Buffer
		router := gin.New()
		router.Use(gin.LoggerWithConfig(gin.LoggerConfig{Output: &access, Skip: skipAccessLog}))
		handler := func(c *gin.Context) { c.Status(status) }
		router.GET(healthProbePath, handler)
		router.POST(healthProbePath, handler)
		router.GET("/api/documents", handler)

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		require.Equal(t, status, rec.Code)
		return access.String()
	}

	t.Run("successful health probe is not logged", func(t *testing.T) {
		assert.Empty(t, run(t, logrus.InfoLevel, http.MethodGet, healthProbePath, http.StatusOK))
	})
	t.Run("failed health probe is logged", func(t *testing.T) {
		assert.Contains(t, run(t, logrus.InfoLevel, http.MethodGet, healthProbePath, http.StatusInternalServerError), healthProbePath)
	})
	t.Run("other requests are logged", func(t *testing.T) {
		assert.Contains(t, run(t, logrus.InfoLevel, http.MethodGet, "/api/documents", http.StatusOK), "/api/documents")
	})
	t.Run("other methods on the probe path are logged", func(t *testing.T) {
		assert.Contains(t, run(t, logrus.InfoLevel, http.MethodPost, healthProbePath, http.StatusOK), healthProbePath)
	})
	t.Run("debug level logs everything", func(t *testing.T) {
		assert.Contains(t, run(t, logrus.DebugLevel, http.MethodGet, healthProbePath, http.StatusOK), healthProbePath)
	})
}
