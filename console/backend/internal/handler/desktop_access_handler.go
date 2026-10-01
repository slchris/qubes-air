package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/desktopaccess"
	"github.com/slchris/qubes-air/console/internal/middleware"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/service"
	"github.com/slchris/qubes-air/console/internal/xpra"
)

// Desktop access bounds (docs/runtime-defaults.md UD-26).
const (
	// desktopAccessBodyLimit caps the two JSON bodies these routes accept
	// ({"operation":"frame"} and {"grant":"<43 chars>"}), well under the API's
	// general 1 MiB limit.
	desktopAccessBodyLimit = 1 << 10
	// desktopWriteSlack is added to a desktop call's own bound to form its
	// write deadline, so the caller receives the answer instead of a cut
	// connection.
	desktopWriteSlack = 5 * time.Second
	// MaxConcurrentDesktopFrames caps captures in flight in this process. One
	// capture can hold a 4 MiB record and decode a PNG of up to 4 Mi pixels
	// (about 34 MB for 16-bit RGBA), so the cap bounds that memory; a capture
	// over it is refused at once, before its grant is consumed.
	MaxConcurrentDesktopFrames = 2
)

// The browser's approval actions carry this header. A cross-site form cannot
// set it, and a cross-origin script cannot either: the CORS allowlist does not
// include it, so the preflight fails.
const (
	desktopActionHeader = "X-Console-Action"
	desktopActionValue  = "desktop-consent"
)

// DesktopQubeLookup is the one read the desktop routes need from the qubes.
type DesktopQubeLookup interface {
	GetByID(ctx context.Context, id string) (*models.Qube, error)
}

// DesktopAccessHandler serves the MCP side of desktop consent (request a
// grant, spend it on one frame) and the Console operator's approval queue.
type DesktopAccessHandler struct {
	store      *desktopaccess.Store
	qubes      DesktopQubeLookup
	frames     service.DesktopFrameCapturer
	frameSlots chan struct{}
}

// NewDesktopAccessHandler builds the handler. frames may be nil, in which
// case every frame request is refused with 503 before an operator is asked.
func NewDesktopAccessHandler(store *desktopaccess.Store, qubes DesktopQubeLookup, frames service.DesktopFrameCapturer) *DesktopAccessHandler {
	return &DesktopAccessHandler{
		store:      store,
		qubes:      qubes,
		frames:     frames,
		frameSlots: make(chan struct{}, MaxConcurrentDesktopFrames),
	}
}

// RegisterRoutes registers the desktop access routes.
func (h *DesktopAccessHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/qubes/:id/desktop-access", h.Request)
	rg.POST("/qubes/:id/desktop-frame", h.Frame)
	rg.GET("/desktop-access", h.Queue)
	rg.POST("/desktop-access/:id/approve", h.Approve)
	rg.POST("/desktop-access/:id/deny", h.Deny)
	rg.POST("/desktop-access/:id/stop", h.Stop)
}

type desktopAccessRequest struct {
	Operation desktopaccess.Operation `json:"operation"`
}

type desktopFrameRequest struct {
	Grant string `json:"grant"`
}

type desktopGrantResponse struct {
	Grant     string                  `json:"grant"`
	RequestID string                  `json:"request_id"`
	QubeID    string                  `json:"qube_id"`
	Operation desktopaccess.Operation `json:"operation"`
	ExpiresAt time.Time               `json:"expires_at"`
}

// Request asks a Console operator for a one-frame grant and holds the call
// open until the operator decides or the approval window closes. The grant is
// returned only here, to the caller that asked.
func (h *DesktopAccessHandler) Request(c *gin.Context) {
	subject, ok := grantCaller(c)
	if !ok {
		return
	}
	var body desktopAccessRequest
	if !decodeDesktopBody(c, &body) {
		return
	}
	if body.Operation != desktopaccess.OperationFrame {
		respondDesktopRefusal(c, http.StatusBadRequest, "only the frame operation is supported")
		return
	}
	if !h.frameAvailable() {
		respondDesktopError(c, service.ErrDesktopTransportUnavailable)
		return
	}
	qube, err := h.readyQube(c.Request.Context(), c.Param("id"))
	if err != nil {
		respondDesktopError(c, err)
		return
	}
	request, err := h.store.Request(subject, qube.ID, desktopaccess.OperationFrame)
	if err != nil {
		respondDesktopError(c, err)
		return
	}
	extendWriteDeadline(c, desktopaccess.ApprovalTTL+desktopWriteSlack)
	secret, granted, err := h.store.WaitDecision(c.Request.Context(), request.ID, subject)
	if err != nil {
		respondDesktopError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, desktopGrantResponse{
		Grant: secret, RequestID: granted.ID, QubeID: granted.QubeID,
		Operation: granted.Operation, ExpiresAt: granted.ExpiresAt,
	})
}

// Frame spends a frame grant on one PNG of the qube's desktop. A grant is
// bound to the caller, the qube and the operation it was approved for, and is
// consumed by the first attempt that gets past the capacity check.
func (h *DesktopAccessHandler) Frame(c *gin.Context) {
	subject, ok := grantCaller(c)
	if !ok {
		return
	}
	if !h.frameAvailable() {
		respondDesktopError(c, service.ErrDesktopTransportUnavailable)
		return
	}
	var body desktopFrameRequest
	if !decodeDesktopBody(c, &body) {
		return
	}
	if body.Grant == "" {
		respondDesktopRefusal(c, http.StatusBadRequest, "grant is required")
		return
	}
	release, ok := h.reserveFrameSlot()
	if !ok {
		c.Header("Retry-After", "1")
		respondDesktopRefusal(c, http.StatusServiceUnavailable, "too many desktop frames are being captured; retry shortly")
		return
	}
	defer release()
	lease, err := h.store.Acquire(body.Grant, subject, c.Param("id"), desktopaccess.OperationFrame)
	if err != nil {
		respondDesktopError(c, err)
		return
	}
	defer lease.Complete()
	extendWriteDeadline(c, desktopaccess.FrameGrantTTL+desktopWriteSlack)
	frame, err := h.frames.CaptureDesktopFrame(c.Request.Context(), c.Param("id"), lease)
	if err == nil && !lease.Valid() {
		err = service.ErrDesktopGrantEnded
	}
	if err != nil {
		respondDesktopError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "image/png", frame.PNG)
}

// Queue lists pending requests and live grants for a Console operator. It
// carries metadata only; no grant appears in it.
func (h *DesktopAccessHandler) Queue(c *gin.Context) {
	if !consentOperator(c, false) {
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"requests": h.store.OperatorQueue()})
}

// Approve issues the grant to the waiting requester, after checking again
// that the qube still has a desktop to read. The operator's response never
// contains the grant.
func (h *DesktopAccessHandler) Approve(c *gin.Context) {
	if !consentOperator(c, true) {
		return
	}
	pending, found := h.store.PendingRequest(c.Param("id"))
	if !found {
		respondDesktopError(c, desktopaccess.ErrNotFound)
		return
	}
	if _, err := h.readyQube(c.Request.Context(), pending.QubeID); err != nil {
		respondDesktopError(c, err)
		return
	}
	request, err := h.store.Approve(pending.ID)
	respondDecision(c, request, err)
}

// Deny refuses a pending request.
func (h *DesktopAccessHandler) Deny(c *gin.Context) {
	if !consentOperator(c, true) {
		return
	}
	request, err := h.store.Deny(c.Param("id"))
	respondDecision(c, request, err)
}

// Stop revokes a pending request or a live grant; an active capture ends at
// once.
func (h *DesktopAccessHandler) Stop(c *gin.Context) {
	if !consentOperator(c, true) {
		return
	}
	if _, err := h.store.Stop(c.Param("id")); err != nil {
		respondDesktopError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func respondDecision(c *gin.Context, request desktopaccess.Request, err error) {
	if err != nil {
		respondDesktopError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, request)
}

func (h *DesktopAccessHandler) frameAvailable() bool {
	return h.frames != nil && h.frames.DesktopFrameAvailable()
}

// reserveFrameSlot takes one of the capture slots without waiting.
func (h *DesktopAccessHandler) reserveFrameSlot() (release func(), ok bool) {
	select {
	case h.frameSlots <- struct{}{}:
		return func() { <-h.frameSlots }, true
	default:
		return nil, false
	}
}

// readyQube loads the qube a request names and refuses one with no desktop
// to read.
func (h *DesktopAccessHandler) readyQube(ctx context.Context, id string) (*models.Qube, error) {
	qube, err := h.qubes.GetByID(ctx, id)
	if err != nil || qube == nil {
		return nil, service.ErrQubeNotFound
	}
	if qube.Status != models.QubeStatusRunning || qube.AgentHealth != models.AgentHealthHealthy {
		return nil, service.ErrDesktopNotReady
	}
	return qube, nil
}

// grantCaller admits a caller that may request or spend a grant: control
// scope with a named subject, over TLS or loopback so the grant is never sent
// in clear over a network. It writes the refusal itself.
func grantCaller(c *gin.Context) (string, bool) {
	subject, ok := controlSubject(c)
	if !ok {
		respondDesktopRefusal(c, http.StatusForbidden, "desktop access requires an authenticated control credential")
		return "", false
	}
	if !secureGrantChannel(c.Request) {
		respondDesktopRefusal(c, http.StatusForbidden, "desktop grants are only delivered over TLS or a loopback connection")
		return "", false
	}
	return subject, true
}

// consentOperator admits a person at the Console: a live browser session with
// control scope and fleet-wide authority. A Bearer token — the credential
// automation holds — never passes, and neither does a console with
// authentication disabled, which has no session at all. mutating actions must
// also carry the explicit action header. The operator's name reaches the audit
// line through the session, like every other action.
func consentOperator(c *gin.Context, mutating bool) bool {
	_, ok := controlSubject(c)
	_, zoneRestricted := middleware.ZoneScopeFromContext(c)
	if !ok || !middleware.SessionAuthenticated(c) || zoneRestricted {
		respondDesktopRefusal(c, http.StatusForbidden, "desktop approvals require a fleet-wide control session in the Console")
		return false
	}
	if mutating && c.GetHeader(desktopActionHeader) != desktopActionValue {
		respondDesktopRefusal(c, http.StatusForbidden, "desktop approval actions must come from the Console page")
		return false
	}
	return true
}

func controlSubject(c *gin.Context) (string, bool) {
	scope, ok := middleware.ScopeFromContext(c)
	if !ok || scope != string(middleware.ScopeControl) {
		return "", false
	}
	subject, ok := middleware.SubjectFromContext(c)
	return subject, ok && subject != ""
}

func secureGrantChannel(request *http.Request) bool {
	if request.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// decodeDesktopBody reads exactly one JSON object with only known fields,
// within desktopAccessBodyLimit, and writes the refusal itself.
func decodeDesktopBody(c *gin.Context, into any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, desktopAccessBodyLimit))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(into)
	if err == nil {
		err = ensureJSONEnd(decoder)
	}
	if err == nil {
		return true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		respondDesktopRefusal(c, http.StatusRequestEntityTooLarge, "request body is too large")
		return false
	}
	respondDesktopRefusal(c, http.StatusBadRequest, "request body must be one JSON object with only the documented fields")
	return false
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

// extendWriteDeadline gives this response its own write deadline. The server
// cuts ordinary writes at 15 s, and these calls legitimately wait longer (an
// approval takes up to 30 s); raising the server-wide timeout for them would
// loosen it for every route. The M1-15 log stream does the same.
func extendWriteDeadline(c *gin.Context, d time.Duration) {
	rc := http.NewResponseController(c.Writer)
	if err := rc.SetWriteDeadline(time.Now().Add(d)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		log.Printf("desktop access: could not extend the write deadline for %s: %v", c.FullPath(), err)
	}
}

// desktopErrorResponse is the fixed answer for one class of failure. The
// detail behind it is logged, never sent: it can name hosts, services and
// remote error text that the caller has no use for.
type desktopErrorResponse struct {
	target  error
	status  int
	message string
}

var desktopErrorResponses = []desktopErrorResponse{
	{desktopaccess.ErrInvalidRequest, http.StatusBadRequest, "invalid desktop access request"},
	{desktopaccess.ErrNotFound, http.StatusNotFound, "no such pending desktop access request"},
	{desktopaccess.ErrAtCapacity, http.StatusTooManyRequests, "too many desktop access requests are waiting; retry later"},
	{desktopaccess.ErrExpired, http.StatusRequestTimeout, "no decision was made within the approval window"},
	{desktopaccess.ErrDenied, http.StatusForbidden, "the desktop access request was denied"},
	{desktopaccess.ErrRevoked, http.StatusForbidden, "desktop access was stopped"},
	{desktopaccess.ErrInvalidGrant, http.StatusForbidden, "the desktop grant is not valid for this request"},
	{desktopaccess.ErrInvalidTransition, http.StatusConflict, "the desktop access request is not in a state that allows this"},
	{service.ErrDesktopGrantEnded, http.StatusForbidden, "desktop access ended before the frame was read"},
	{service.ErrQubeNotFound, http.StatusNotFound, "qube not found"},
	{service.ErrDesktopNotReady, http.StatusPreconditionFailed, "the qube must be running with a healthy agent"},
	{service.ErrZoneDisconnected, http.StatusPreconditionFailed, "the qube's zone is disconnected"},
	{service.ErrZoneNotFound, http.StatusPreconditionFailed, "the qube's zone is not available"},
	{service.ErrDesktopTransportUnavailable, http.StatusServiceUnavailable, "desktop frame capture is not available on this console"},
	{service.ErrDesktopUnreachable, http.StatusBadGateway, "the qube's desktop could not be reached"},
	{xpra.ErrScreenshotUnsupported, http.StatusBadGateway, "the qube's desktop server did not honor the screenshot request"},
	{xpra.ErrEmptyScreenshot, http.StatusConflict, "the qube's desktop has no window to capture"},
	{xpra.ErrAuthenticationRequired, http.StatusBadGateway, "the qube's desktop server asked for authentication this console does not support"},
	{xpra.ErrUnsupportedUpgrade, http.StatusBadGateway, "the qube's desktop server asked for a transport upgrade this console does not support"},
	{xpra.ErrScreenshotSession, http.StatusBadGateway, "the qube's desktop frame could not be read"},
	{context.DeadlineExceeded, http.StatusGatewayTimeout, "the desktop frame capture timed out"},
	{context.Canceled, http.StatusRequestTimeout, "the request ended before desktop access completed"},
}

// respondDesktopError maps err onto a fixed status and message and logs the
// detail. Nothing from err reaches the response.
func respondDesktopError(c *gin.Context, err error) {
	for _, r := range desktopErrorResponses {
		if errors.Is(err, r.target) {
			log.Printf("desktop access: %s %s: %d: %v", c.Request.Method, c.FullPath(), r.status, err)
			respondDesktopRefusal(c, r.status, r.message)
			return
		}
	}
	log.Printf("desktop access: %s %s: unexpected failure: %v", c.Request.Method, c.FullPath(), err)
	respondDesktopRefusal(c, http.StatusInternalServerError, "desktop access failed")
}

func respondDesktopRefusal(c *gin.Context, status int, message string) {
	c.Header("Cache-Control", "no-store")
	respondError(c, status, errors.New(message))
}
