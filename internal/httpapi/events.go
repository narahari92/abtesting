package httpapi

import (
	"encoding/json"
	"io"
	"net/http"

	"variantsvc/internal/events"
)

// eventBodyLimit bounds a beacon. Real beacons are under 300 bytes.
const eventBodyLimit = 8 << 10

// readBeacon parses a beacon body as JSON whatever its declared content
// type: navigator.sendBeacon sends text/plain so the request stays a CORS
// simple request with no preflight.
func readBeacon(r *http.Request, v any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, eventBodyLimit+1))
	if err != nil || len(body) == 0 || len(body) > eventBodyLimit {
		return false
	}
	return json.Unmarshal(body, v) == nil
}

func credentials(r *http.Request) events.Credentials {
	return events.Credentials{Origin: r.Header.Get("Origin"), APIKey: bearerToken(r)}
}

// POST /v1/events/exposure. Always 202 with an empty body: the caller is a
// customer's page and never receives an error status from the write path.
func (s *Server) handleExposure(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusAccepted)
	var e events.Exposure
	if !readBeacon(r, &e) {
		s.Log.Info("event", "kind", "exposure", "reason", "unparseable")
		return
	}
	s.Events.Exposure(r.Context(), e, credentials(r))
}

// POST /v1/events/conversion. Same contract as exposure.
func (s *Server) handleConversion(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusAccepted)
	var c events.Conversion
	if !readBeacon(r, &c) {
		s.Log.Info("event", "kind", "conversion", "reason", "unparseable")
		return
	}
	s.Events.Conversion(r.Context(), c, credentials(r))
}
