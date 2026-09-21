package middleware

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestBodyLimitRejectsOversizedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BodyLimit(16))
	r.POST("/x", func(c *gin.Context) {
		var body map[string]any
		if err := c.ShouldBindJSON(&body); err != nil {
			// Mirrors handler.respondError's MaxBytesError -> 413 mapping.
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				c.Status(http.StatusRequestEntityTooLarge)
				return
			}
			c.Status(http.StatusBadRequest)
			return
		}
		c.Status(http.StatusOK)
	})

	// Valid JSON whose encoded size exceeds the 16-byte limit: the decoder must
	// read past the cap, which is the case MaxBytesReader exists for.
	oversized := append([]byte(`{"k":"`), bytes.Repeat([]byte("a"), 64)...)
	oversized = append(oversized, '"', '}')
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(oversized))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: got status %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestBodyLimitAllowsBodyWithinLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BodyLimit(64))
	r.POST("/x", func(c *gin.Context) {
		var body map[string]any
		if err := c.ShouldBindJSON(&body); err != nil {
			c.Status(http.StatusBadRequest)
			return
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader([]byte(`{"k":"v"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("in-limit body: got status %d, want %d", w.Code, http.StatusOK)
	}
}

func TestBodyLimitFallsBackToDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BodyLimit(0))
	r.POST("/x", func(c *gin.Context) {
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				c.Status(http.StatusRequestEntityTooLarge)
				return
			}
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(bytes.Repeat([]byte("a"), int(defaultMaxBodyBytes)+1)))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// A non-positive limit must still bound the body, not disable the cap.
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("default fallback: got status %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
	}
}
