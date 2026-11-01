package metadata

import (
	"reflect"
	"testing"
)

func TestExtractsTokensInOrderWithDuplicates(t *testing.T) {
	got := ExpressionTokens("{a} and {b} and {a}")
	want := []string{"a", "b", "a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNoTokensInPlainText(t *testing.T) {
	if len(ExpressionTokens("plain text")) != 0 {
		t.Fatal("expected no tokens")
	}
}

func TestUnterminatedBraceIsNotAToken(t *testing.T) {
	if len(ExpressionTokens("plain {oops text")) != 0 {
		t.Fatal("expected no tokens")
	}
}

func TestRendersKnownAndMissingTokens(t *testing.T) {
	out := RenderExpression("{firstName} {lastName}", func(name string) (string, bool) {
		if name == "firstName" {
			return "Ada", true
		}
		return "", false
	})
	if out != "Ada " {
		t.Fatalf("got %q", out)
	}
}

func TestUnterminatedBraceIsKeptLiteralWhenRendering(t *testing.T) {
	out := RenderExpression("hi {there", func(string) (string, bool) { return "x", true })
	if out != "hi {there" {
		t.Fatalf("got %q", out)
	}
}
