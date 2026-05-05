package requests

import "fmt"

// HttpError represents a non-successful HTTP response.
type HttpError struct {
	StatusCode int
	Body       string
	err        error
}

func (e *HttpError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("http error %d: %s: %v", e.StatusCode, e.Body, e.err)
	}
	return fmt.Sprintf("http error %d: %s", e.StatusCode, e.Body)
}

func (e *HttpError) Unwrap() error {
	return e.err
}
