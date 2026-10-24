// Package desktop implements the fixed local protocol between the sandboxed
// qrexec services and an explicitly enabled, unprivileged desktop session.
package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	operationLaunch  = "launch"
	SocketPath       = "/run/qubes-air-desktop-broker/control.sock"
	WorkerPath       = "/usr/lib/qubes-air/desktop-worker.py"
	DesktopUID       = 1000
	MaxRequestBytes  = 512
	MaxResponseBytes = 1 << 20
	MaxApps          = 256
)

var ErrProtocol = errors.New("invalid desktop protocol message")

var appPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,113}$`)
var displayPattern = regexp.MustCompile(`^:[0-9]{1,5}(\.[0-9]{1,2})?$`)
var iconPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Request deliberately has no command, path, environment or target fields.
type Request struct {
	Operation string `json:"operation"`
	App       string `json:"app,omitempty"`
}

// Response acknowledges acceptance only; it does not assert window visibility.
type Response struct {
	OK      bool   `json:"ok"`
	App     string `json:"app,omitempty"`
	Display string `json:"display,omitempty"`
	Menu    string `json:"menu,omitempty"`
	Code    int    `json:"code,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ValidAppID shares the Relay's 128-byte bound with the 15-byte service prefix.
func ValidAppID(app string) bool {
	return appPattern.MatchString(app) && app != "." && !strings.Contains(app, "..")
}

func validateRequest(request Request) error {
	if request.Operation == "menus" && request.App == "" {
		return nil
	}
	if request.Operation == operationLaunch && ValidAppID(request.App) {
		return nil
	}
	return ErrProtocol
}

func decodeMessage(reader io.Reader, limit int64, value any) error {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(data)) > limit || !utf8.Valid(data) {
		return ErrProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return ErrProtocol
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrProtocol
	}
	return nil
}

func decodeRequest(reader io.Reader) (Request, error) {
	var request Request
	if err := decodeMessage(reader, MaxRequestBytes, &request); err != nil {
		return Request{}, err
	}
	return request, validateRequest(request)
}

func validateResponse(request Request, reply Response) error {
	if !reply.OK {
		if reply.App != "" || reply.Display != "" || reply.Menu != "" || !knownFailure(reply) {
			return ErrProtocol
		}
		return nil
	}
	if reply.Code != 0 || reply.Error != "" {
		return ErrProtocol
	}
	if request.Operation == operationLaunch {
		if reply.App != request.App || !displayPattern.MatchString(reply.Display) || reply.Menu != "" {
			return ErrProtocol
		}
		return nil
	}
	if reply.App != "" || reply.Display != "" {
		return ErrProtocol
	}
	return validateMenu(reply.Menu)
}

func cleanText(value string) bool {
	if len(value) > 2048 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validateMenu(menu string) error {
	if len(menu) > MaxResponseBytes || strings.Count(menu, "\n") > MaxApps*5 {
		return ErrProtocol
	}
	entries := make(map[string]map[string]bool)
	for _, line := range strings.Split(strings.TrimSuffix(menu, "\n"), "\n") {
		if line == "" {
			continue
		}
		app, field, ok := strings.Cut(line, ":")
		if !ok || !ValidAppID(app) {
			return ErrProtocol
		}
		key, value, ok := strings.Cut(field, "=")
		if !ok || !validMenuField(app, key, value) {
			return ErrProtocol
		}
		if entries[app] == nil {
			entries[app] = make(map[string]bool)
		}
		if entries[app][key] || len(entries) > MaxApps {
			return ErrProtocol
		}
		entries[app][key] = true
	}
	for _, fields := range entries {
		if !completeMenuFields(fields) {
			return ErrProtocol
		}
	}
	return nil
}

func completeMenuFields(fields map[string]bool) bool {
	return fields["Name"] && fields["Type"] && fields["Exec"]
}

func encodeResponse(reply Response) (Response, []byte) {
	data, _ := json.Marshal(reply)
	if len(data) > MaxResponseBytes {
		reply = failure(75)
		data, _ = json.Marshal(reply)
	}
	return reply, data
}

func validMenuField(app, key, value string) bool {
	switch key {
	case "Name", "Comment":
		return value != "" && cleanText(value)
	case "Exec":
		return value == "qubes-desktop-run "+app
	case "Type":
		return value == "Application"
	case "Icon":
		return iconPattern.MatchString(value)
	default:
		return false
	}
}

func failure(code int) Response {
	messages := map[int]string{
		64: "invalid desktop request", 69: "desktop session unavailable",
		70: "desktop operation failed", 75: "desktop operation incomplete; do not retry automatically",
		77: "desktop peer is not authorized",
	}
	return Response{Code: code, Error: messages[code]}
}

func knownFailure(reply Response) bool {
	expected := failure(reply.Code)
	return expected.Error != "" && expected.Error == reply.Error
}
