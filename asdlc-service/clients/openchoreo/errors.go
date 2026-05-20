package openchoreo

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo/gen"
)

// Sentinel errors for OpenChoreo HTTP semantics. Callers in services/ and
// controllers/ branch on these via errors.Is — they're public so the
// service-layer doesn't have to know about HTTP status codes.
//
// Mirrors agent-manager's `utils/errors.go` HTTP-error block; kept inside
// the openchoreo package here because the OC client is the only producer.
// If a second client adopts the same scheme, hoist them to a shared package.
var (
	ErrBadRequest          = errors.New("bad request")
	ErrUnauthorized        = errors.New("unauthorized")
	ErrForbidden           = errors.New("forbidden")
	ErrNotFound            = errors.New("not found")
	ErrConflict            = errors.New("conflict")
	ErrInternalServerError = errors.New("internal server error")
)

// ErrorResponses holds the typed error fields a gen response can carry.
// Callers populate only the fields the underlying endpoint declares —
// e.g. CreateProject has no JSON404, so leave JSON404 nil. Matches
// agent-manager/clients/openchoreosvc/client/errors.go.
type ErrorResponses struct {
	JSON400 *gen.BadRequest
	JSON401 *gen.Unauthorized
	JSON403 *gen.Forbidden
	JSON404 *gen.NotFound
	JSON409 *gen.Conflict
	JSON500 *gen.InternalError
}

// handleErrorResponse turns the populated typed-error fields into a
// sentinel-wrapped error: callers errors.Is(err, ErrNotFound) and the human
// message stays accessible via err.Error(). The OC error body's `.Error`
// string is the human description; `.Details` is logged via logErrorDetails.
func handleErrorResponse(statusCode int, errs ErrorResponses) error {
	switch {
	case errs.JSON400 != nil:
		logErrorDetails(errs.JSON400)
		return fmt.Errorf("%w: %s", ErrBadRequest, errs.JSON400.Error)
	case errs.JSON401 != nil:
		logErrorDetails(errs.JSON401)
		return fmt.Errorf("%w: %s", ErrUnauthorized, errs.JSON401.Error)
	case errs.JSON403 != nil:
		logErrorDetails(errs.JSON403)
		return fmt.Errorf("%w: %s", ErrForbidden, errs.JSON403.Error)
	case errs.JSON404 != nil:
		logErrorDetails(errs.JSON404)
		return fmt.Errorf("%w: %s", ErrNotFound, errs.JSON404.Error)
	case errs.JSON409 != nil:
		logErrorDetails(errs.JSON409)
		return fmt.Errorf("%w: %s", ErrConflict, errs.JSON409.Error)
	case errs.JSON500 != nil:
		logErrorDetails(errs.JSON500)
		return fmt.Errorf("%w: %s", ErrInternalServerError, errs.JSON500.Error)
	}
	// Fall-through: gen produced no typed match (gateway-shaped error,
	// schema mismatch, etc.). Synthesize an error from the bare status so
	// callers can still branch on the sentinel for the obvious codes.
	switch statusCode {
	case 400:
		return fmt.Errorf("%w: status %d", ErrBadRequest, statusCode)
	case 401:
		return fmt.Errorf("%w: status %d", ErrUnauthorized, statusCode)
	case 403:
		return fmt.Errorf("%w: status %d", ErrForbidden, statusCode)
	case 404:
		return fmt.Errorf("%w: status %d", ErrNotFound, statusCode)
	case 409:
		return fmt.Errorf("%w: status %d", ErrConflict, statusCode)
	case 500:
		return fmt.Errorf("%w: status %d", ErrInternalServerError, statusCode)
	default:
		return fmt.Errorf("openchoreo: unexpected status %d", statusCode)
	}
}

// logErrorDetails surfaces gen.ErrorResponse.Details (per-field validation
// messages, when present) at debug level.
func logErrorDetails(e *gen.ErrorResponse) {
	if e == nil || e.Details == nil {
		return
	}
	for _, d := range *e.Details {
		slog.Debug("openchoreo error detail",
			"field", derefStr(d.Field),
			"message", derefStr(d.Message),
		)
	}
}
