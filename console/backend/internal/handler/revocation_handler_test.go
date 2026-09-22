package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type statusSource func(context.Context) ([]byte, error)

func (f statusSource) RevocationDocument(ctx context.Context) ([]byte, error) { return f(ctx) }

func TestPublicRevocationEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, fail := range []bool{false, true} {
		r := gin.New()
		calls := 0
		RegisterRevocations(r, statusSource(func(ctx context.Context) ([]byte, error) {
			calls++
			_, ok := ctx.Deadline()
			require.True(t, ok)
			if fail {
				return nil, errors.New("private detail")
			}
			return []byte(`{"signed":"public-status"}`), nil
		}))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/pki/revocations", nil))
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		if fail {
			require.Equal(t, 503, w.Code)
			require.NotContains(t, w.Body.String(), "private")
		} else {
			require.Equal(t, 200, w.Code)
		}
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/pki/revocations", strings.NewReader("body")))
		require.Equal(t, 413, w.Code)
		require.Equal(t, 1, calls)
	}
}
