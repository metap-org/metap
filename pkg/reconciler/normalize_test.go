package reconciler

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLowercasesStripsQuotesParensAndCollapsesWhitespace(t *testing.T) {
	got := NormalizeExpr(`(jsonb_extract_path_text("data",  'departmentId'))`)
	want := `jsonb_extract_path_textdata, 'departmentid'`
	require.Equal(t, want, got, "got %q, want %q", got, want)
}

func TestStripsABareCastWithNoParenArgs(t *testing.T) {
	got := NormalizeExpr(`(data ->> 'field')::text`)
	require.Equal(t, `data ->> 'field'`, got, "got %q", got)
}

func TestStripsACastWithParenArgsLikeNumeric(t *testing.T) {
	got := NormalizeExpr(`(data ->> 'amount')::numeric(18,4)`)
	require.Equal(t, `data ->> 'amount'`, got, "got %q", got)
}

func TestMatchesWhatPostgresEchoesBackForATextExpressionIndex(t *testing.T) {
	typed := NormalizeExpr(`(data ->> 'field')::text`)
	echoed := NormalizeExpr(`((data ->> 'field'::text))`)
	require.Equal(t, echoed, typed, "typed=%q echoed=%q", typed, echoed)
}

func TestMatchesWhatPostgresEchoesBackForAUuidExpressionIndex(t *testing.T) {
	typed := NormalizeExpr(`(data ->> 'refid')::uuid`)
	echoed := NormalizeExpr(`(((data ->> 'refid'::text))::uuid)`)
	require.Equal(t, echoed, typed, "typed=%q echoed=%q", typed, echoed)
}

func TestMatchesWhatPostgresEchoesBackForAnFtsGinIndex(t *testing.T) {
	typed := NormalizeExpr(`to_tsvector('simple', (data ->> 'description'))`)
	echoed := NormalizeExpr(`to_tsvector('simple'::regconfig, (data ->> 'description'::text))`)
	require.Equal(t, echoed, typed, "typed=%q echoed=%q", typed, echoed)
}

func TestMatchesWhatPostgresEchoesBackForATrigramGinIndex(t *testing.T) {
	typed := NormalizeExpr(`(data ->> 'title') gin_trgm_ops`)
	echoed := NormalizeExpr(`((data ->> 'title'::text)) gin_trgm_ops`)
	require.Equal(t, echoed, typed, "typed=%q echoed=%q", typed, echoed)
}
