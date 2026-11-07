package grpcapi

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
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
		require.FailNow(t, fmt.Sprintf("got %v", st.Code()))
	}

	details, ok := errorDetailsFromTrailer(md)
	require.True(t, ok, "expected details to be set")
	require.EqualValues(t, 422, details.Status, "got %+v", details)
	require.Equal(t, "validation_failed", details.Error, "got %+v", details)
	require.NotNil(t, details.Message, "got %v", details.Message)
	require.Equal(t, "Validation failed", *details.Message, "got %v", details.Message)
	recovered := details.FieldErrors
	require.Len(t, recovered, 2, "field errors must survive, got %+v", recovered)
	require.Len(t, recovered["name"], 1, "field errors must survive, got %+v", recovered)
	require.Equal(t, "is required", recovered["name"][0], "field errors must survive, got %+v", recovered)
}

// 400 and 422 share one gRPC Code, so the numeric status is only recoverable from the details.
func TestTheExactNumericStatusSurvivesEvenWhenTheCodeIsAmbiguous(t *testing.T) {
	for _, httpStatus := range []uint16{400, 422} {
		err, md := errorToStatus(httpStatus, "bad", nil, nil)
		st, _ := status.FromError(err)
		if st.Code() != codes.InvalidArgument {
			require.FailNow(t, fmt.Sprintf("got %v", st.Code()))
		}
		details, ok := errorDetailsFromTrailer(md)
		require.True(t, ok, "got %+v, ok=%v", details, ok)
		require.Equal(t, httpStatus, details.Status, "got %+v, ok=%v", details, ok)
	}
}

func TestTheMessageStillReadsTheSameForACallerThatIgnoresDetails(t *testing.T) {
	withMessage, _ := errorToStatus(404, "not_found", strPtr("No such record"), nil)
	if st, _ := status.FromError(withMessage); st.Message() != "No such record" {
		require.FailNow(t, fmt.Sprintf("got %q", st.Message()))
	}
	// No message -> the stable error code is the human-facing text, unchanged from before.
	withoutMessage, _ := errorToStatus(404, "not_found", nil, nil)
	if st, _ := status.FromError(withoutMessage); st.Message() != "not_found" {
		require.FailNow(t, fmt.Sprintf("got %q", st.Message()))
	}
}

func TestAStatusWithNoDetailsIsReportedAsAbsentNotAsAnError(t *testing.T) {
	_, ok := errorDetailsFromTrailer(nil)
	require.False(t, ok, "expected no details on a nil trailer")
}
