package crud

// PageInfo mirrors the Rust PageInfo.
type PageInfo struct {
	Limit      int64   `json:"limit"`
	NextCursor *string `json:"nextCursor"`
}

// ServiceResult mirrors the Rust ServiceResult<T> enum — Go has no sum type, so this is a plain
// struct with an IsOk discriminant instead; callers must check IsOk before reading Data/Page, or
// Status/Error/Message/FieldErrors otherwise, mirroring a Rust match on the two variants.
type ServiceResult[T any] struct {
	IsOk bool

	// Set when IsOk is true.
	Data T
	Page *PageInfo

	// Set when IsOk is false.
	Status      uint16
	ErrorCode   string
	Message     *string
	FieldErrors map[string][]string
}

func ResultOk[T any](data T) ServiceResult[T] {
	return ServiceResult[T]{IsOk: true, Data: data}
}

func ResultOkWithPage[T any](data T, page PageInfo) ServiceResult[T] {
	return ServiceResult[T]{IsOk: true, Data: data, Page: &page}
}

func ResultErr[T any](status uint16, errorCode string) ServiceResult[T] {
	return ServiceResult[T]{Status: status, ErrorCode: errorCode}
}

func ResultErrWithMessage[T any](status uint16, errorCode, message string) ServiceResult[T] {
	return ServiceResult[T]{Status: status, ErrorCode: errorCode, Message: &message}
}

func ResultErrWithFieldErrors[T any](status uint16, errorCode string, fieldErrors map[string][]string) ServiceResult[T] {
	return ServiceResult[T]{Status: status, ErrorCode: errorCode, FieldErrors: fieldErrors}
}

// ResultErrWithMessageAndFieldErrors carries both a human-readable Message (for a client that
// just shows it as-is) and structured FieldErrors (for a client that wants to do more) on the
// same error — used by delete()'s record_referenced guard, which needs a readable summary of the
// first blocker *and* the full list every blocker maps to.
func ResultErrWithMessageAndFieldErrors[T any](status uint16, errorCode, message string, fieldErrors map[string][]string) ServiceResult[T] {
	return ServiceResult[T]{Status: status, ErrorCode: errorCode, Message: &message, FieldErrors: fieldErrors}
}

// resultErrFrom copies another result's error shape onto a different T — the Go equivalent of
// Rust's `ServiceResult::Err { status, error, message, field_errors }` struct-literal propagation
// used e.g. in check_record_permission's callers.
func resultErrFrom[From, To any](r ServiceResult[From]) ServiceResult[To] {
	return ServiceResult[To]{
		Status:      r.Status,
		ErrorCode:   r.ErrorCode,
		Message:     r.Message,
		FieldErrors: r.FieldErrors,
	}
}
