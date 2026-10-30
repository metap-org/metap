package grpcapi

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func strPtr(s string) *string { return &s }

// The regression this guards: a validation failure must survive the gRPC hop with its per-field
// detail intact, not reduced to a count in a sentence.
func TestFieldErrorsRoundTripThroughAStatus(t *testing.T) {
	fieldErrors := map[string][]string{
		"name":  {"is required"},
		"email": {"must be an email"},
	}
	err, md := errorToStatus(422, "validation_failed", strPtr("Validation failed"), fieldErrors)
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("got %v", st.Code())
	}

	details, ok := errorDetailsFromTrailer(md)
	if !ok {
		t.Fatal("expected details to be set")
	}
	if details.Status != 422 || details.Error != "validation_failed" {
		t.Fatalf("got %+v", details)
	}
	if details.Message == nil || *details.Message != "Validation failed" {
		t.Fatalf("got %v", details.Message)
	}
	recovered := details.FieldErrors
	if len(recovered) != 2 || len(recovered["name"]) != 1 || recovered["name"][0] != "is required" {
		t.Fatalf("field errors must survive, got %+v", recovered)
	}
}

// 400 and 422 share one gRPC Code, so the numeric status is only recoverable from the details.
func TestTheExactNumericStatusSurvivesEvenWhenTheCodeIsAmbiguous(t *testing.T) {
	for _, httpStatus := range []uint16{400, 422} {
		err, md := errorToStatus(httpStatus, "bad", nil, nil)
		st, _ := status.FromError(err)
		if st.Code() != codes.InvalidArgument {
			t.Fatalf("got %v", st.Code())
		}
		details, ok := errorDetailsFromTrailer(md)
		if !ok || details.Status != httpStatus {
			t.Fatalf("got %+v, ok=%v", details, ok)
		}
	}
}

func TestTheMessageStillReadsTheSameForACallerThatIgnoresDetails(t *testing.T) {
	withMessage, _ := errorToStatus(404, "not_found", strPtr("No such record"), nil)
	if st, _ := status.FromError(withMessage); st.Message() != "No such record" {
		t.Fatalf("got %q", st.Message())
	}
	// No message -> the stable error code is the human-facing text, unchanged from before.
	withoutMessage, _ := errorToStatus(404, "not_found", nil, nil)
	if st, _ := status.FromError(withoutMessage); st.Message() != "not_found" {
		t.Fatalf("got %q", st.Message())
	}
}

func TestAStatusWithNoDetailsIsReportedAsAbsentNotAsAnError(t *testing.T) {
	if _, ok := errorDetailsFromTrailer(nil); ok {
		t.Fatal("expected no details on a nil trailer")
	}
}
