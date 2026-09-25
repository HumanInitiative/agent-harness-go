package httpapi

import "net/http"

// Healthz handles GET /healthz — liveness: "is the process alive". It
// checks nothing else on purpose: a failing liveness probe gets the process
// restarted, which would not fix a broken dependency.
//
//	@Summary	Liveness probe
//	@Tags		system
//	@Produce	json
//	@Success	200	{object}	StatusResponse
//	@Router		/healthz [get]
func Healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, StatusResponse{Status: "ok"})
}

// Readyz handles GET /readyz — readiness: "should traffic be routed here".
// This build has no external dependency worth probing on every check (the
// conversation store is in-process, and pinging the model provider would
// spend quota), so it answers like Healthz. It stays a separate endpoint so
// that when a real dependency is added — a database-backed
// ConversationStore, say — its check has an obvious home.
//
//	@Summary	Readiness probe
//	@Tags		system
//	@Produce	json
//	@Success	200	{object}	StatusResponse
//	@Router		/readyz [get]
func Readyz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, StatusResponse{Status: "ready"})
}
