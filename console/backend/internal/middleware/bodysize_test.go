package middleware

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// unknownLenReader hides a reader's length so the HTTP layer can neither size
// the body nor set a Content-Length header — the middleware must not rely on
// declared length to enforce the limit.
type unknownLenReader struct{ r io.Reader }

func (u unknownLenReader) Read(p []byte) (int, error) { return u.r.Read(p) }

func newLimitRouter(limit int64, downstream gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/echo", MaxBodySize(limit), downstream)
	return r
}

// rawEcho reads whatever body reached the handler and sends it back so tests
// can assert the middleware preserved the original bytes.
func rawEcho(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"received": len(body), "bytes": string(body)})
}

func sendPost(r *gin.Engine, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/echo", body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestMaxBodySize_WithinLimitPassesThrough(t *testing.T) {
	const limit int64 = 64
	r := newLimitRouter(limit, rawEcho)

	payload := `{"name":"demo-qube","cpu":2}`
	w := sendPost(r, strings.NewReader(payload))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, fmt.Sprintf(`{"bytes":%q,"received":%d}`, payload, len(payload)), w.Body.String())
}

func TestMaxBodySize_NilBodyPassesThrough(t *testing.T) {
	r := newLimitRouter(64, rawEcho)

	w := sendPost(r, nil)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"bytes":"","received":0}`, w.Body.String())
}

func TestMaxBodySize_ExactlyAtLimitPassesThrough(t *testing.T) {
	const limit int64 = 64
	r := newLimitRouter(limit, rawEcho)

	payload := strings.Repeat("x", int(limit))
	w := sendPost(r, strings.NewReader(payload))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, fmt.Sprintf(`{"bytes":%q,"received":%d}`, payload, len(payload)), w.Body.String())
}

func TestMaxBodySize_OverLimitRejected(t *testing.T) {
	const limit int64 = 64
	r := newLimitRouter(limit, rawEcho)

	w := sendPost(r, strings.NewReader(strings.Repeat("x", int(limit)+1)))

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	assert.JSONEq(t, `{"code":413,"error":"Request Entity Too Large"}`, w.Body.String())
}

func TestMaxBodySize_OverLimitWithNoContentLength(t *testing.T) {
	const limit int64 = 64
	r := newLimitRouter(limit, rawEcho)

	req := httptest.NewRequest(http.MethodPost, "/echo",
		unknownLenReader{r: strings.NewReader(strings.Repeat("x", int(limit)+1))})
	assert.LessOrEqual(t, req.ContentLength, int64(0), "test body must not carry a usable Content-Length")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	assert.JSONEq(t, `{"code":413,"error":"Request Entity Too Large"}`, w.Body.String())
}

func TestMaxBodySize_MalformedBodyPassesThroughToHandler(t *testing.T) {
	const limit int64 = 64
	downstream := func(c *gin.Context) {
		var v struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&v); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"name": v.Name})
	}
	r := newLimitRouter(limit, downstream)

	// Body fits the limit but is not valid JSON: the middleware must let it
	// reach the handler untouched, which rejects it with its usual 400.
	w := sendPost(r, strings.NewReader(`{"name":"`))

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "error")
}