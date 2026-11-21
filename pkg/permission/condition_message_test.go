package permission

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConditionMessageRoundTripsThroughJSON(t *testing.T) {
	c := AllCondition(
		AttributeCondition("a", OpEq, LiteralValue(json.RawMessage("1"))).WithMessage("A must be 1"),
	).WithMessage("group")
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	assert.JSONEq(t, `{"all":[{"attribute":"a","op":"eq","value":{"literal":1},"message":"A must be 1"}],"message":"group"}`, string(raw))

	var back PolicyCondition
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, "group", back.Message)
	require.Len(t, back.All, 1)
	assert.Equal(t, "A must be 1", back.All[0].Message)

	// No message: the JSON is exactly what it was before messages existed.
	plain, err := json.Marshal(AttributeCondition("a", OpEq, LiteralValue(json.RawMessage("1"))))
	require.NoError(t, err)
	assert.JSONEq(t, `{"attribute":"a","op":"eq","value":{"literal":1}}`, string(plain))
}

func TestFailureNamesTheLeafAndKeepsTheTechnicalReason(t *testing.T) {
	c := AnyCondition(AttributeCondition("status", OpEq, LiteralValue(json.RawMessage(`"ok"`))))
	result := EvaluateCondition(c, json.RawMessage(`{"status":"bad"}`), &RequestContext{})
	require.False(t, result.IsPassed())
	f, ok := result.Failure()
	require.True(t, ok)
	assert.Equal(t, "status", f.Attribute)
	assert.JSONEq(t, `"bad"`, string(f.Actual))
	reason, _ := result.Reason()
	assert.Contains(t, reason, "condition failed", "callers that only read Reason keep the old text")
}

func TestMessageKeyRoundTripsThroughJSON(t *testing.T) {
	c := AllCondition(
		AttributeCondition("a", OpEq, LiteralValue(json.RawMessage("1"))).WithMessage("m").WithMessageKey("k.leaf"),
	).WithMessageKey("k.group")
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	var back PolicyCondition
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, "k.group", back.MessageKey)
	assert.Equal(t, "k.leaf", back.All[0].MessageKey)
}
