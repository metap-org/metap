package graphqlgateway

import (
	"github.com/metap-org/metap/pkg/crud"
)

// serviceError carries a CrudService-shaped failure (status/code/message/fieldErrors) through
// graphql-go's error path as a GraphQL extensions object — the same extensions.code/status/
// fieldErrors shape every other transport in this migration uses, so a frontend's error
// handling doesn't need a gateway-specific branch.
type serviceError struct {
	status      uint16
	code        string
	message     string
	fieldErrors map[string][]string
	details     map[string]any
}

func newServiceError(status uint16, code string, message *string, fieldErrors map[string][]string) *serviceError {
	msg := code
	if message != nil {
		msg = *message
	}
	return &serviceError{status: status, code: code, message: msg, fieldErrors: fieldErrors}
}

// Error is the GraphQL error's message: the human text only. The code is already in
// extensions.code, and prefixing it ("guard_failed: …") put an identifier in front of every
// message a frontend shows.
func (e *serviceError) Error() string {
	return e.message
}

// Extensions is picked up by graphql-go's gqlerrors formatting when an error returned from a
// resolver implements this interface.
func (e *serviceError) Extensions() map[string]interface{} {
	ext := map[string]interface{}{"code": e.code, "status": e.status}
	if e.fieldErrors != nil {
		ext["fieldErrors"] = e.fieldErrors
	}
	if e.details != nil {
		ext["details"] = e.details
	}
	return ext
}

// gqlError turns what a backend returned into the error a GraphQL resolver hands back: a
// *crud.ServiceError becomes a serviceError whose extensions carry code/status/fieldErrors, any
// other error passes through unchanged (graphql-go reports it as a plain message).
func gqlError(err error) error {
	if se, ok := crud.AsServiceError(err); ok {
		var msg *string
		if se.Message != "" {
			msg = &se.Message
		}
		e := newServiceError(se.Status, se.Code, msg, se.FieldErrors)
		e.details = se.Details
		return e
	}
	return err
}
