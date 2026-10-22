package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

// defaultRetryAfter is suggested to clients when the model provider is
// unavailable but did not say how long to wait.
const defaultRetryAfter = 5 * time.Second

// decodeJSON reads exactly one JSON object with no unknown fields into dst.
// It writes the error response itself and reports false when the body is
// unusable; shape is an example shown in the 400 message.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, shape string) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	err := dec.Decode(dst)
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errors.New("unexpected data after the JSON object")
	}
	if err == nil {
		return true
	}

	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, r, http.StatusRequestEntityTooLarge, codeRequestTooLarge,
			"request body exceeds "+strconv.FormatInt(tooLarge.Limit, 10)+" bytes")
		return false
	}
	writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
		"request body must be a single JSON object like "+shape+": "+err.Error())
	return false
}

// writeCommonError answers the failures every endpoint that calls the model
// can have: the client leaving, the request timing out, the provider being
// unavailable. It reports whether it handled err.
func writeCommonError(logger *slog.Logger, w http.ResponseWriter, r *http.Request, err error) bool {
	ctx := r.Context()
	var unavailable *outbound.ModelUnavailableError

	switch {
	case errors.Is(err, context.Canceled) && ctx.Err() != nil:
		logger.InfoContext(ctx, "client disconnected before the reply was ready")
		writeError(w, r, statusClientClosedRequest, codeClientClosed, "client closed request")

	case errors.Is(err, context.DeadlineExceeded):
		logger.WarnContext(ctx, "request timed out", "error", err)
		writeError(w, r, http.StatusGatewayTimeout, codeTimeout, "the agent did not reply in time")

	case errors.As(err, &unavailable):
		retryAfter := unavailable.RetryAfter
		if retryAfter <= 0 {
			retryAfter = defaultRetryAfter
		}
		logger.WarnContext(ctx, "model provider unavailable", "error", err, "retry_after", retryAfter)
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
		writeError(w, r, http.StatusServiceUnavailable, codeModelUnavailable,
			"the model provider is temporarily unavailable, retry later")

	default:
		return false
	}
	return true
}
