package metadata

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A cross-service link names its target entity, which this registry does not have; it must not
// make the registry invalid (a Reference to an unknown entity does).
func TestRefersToIsACrossServiceLinkNotAReference(t *testing.T) {
	jobs := EntityDefinition{Name: "waf.scan_jobs", Label: "Scan job", TableName: "waf.waf_scan_jobs", Fields: []EntityField{
		NewField("zoneId", "Zone", KindString).Require().RefersTo("waf.zones"),
	}}
	reg := NewMetadataRegistry()
	require.NoError(t, reg.Register(jobs))
	require.NoError(t, reg.ValidateReferences(), "the target is another service's entity")

	f := jobs.Fields[0]
	assert.Equal(t, KindString, f.Kind, "still a plain string column")
	require.NotNil(t, f.RefEntity)
	assert.Equal(t, "waf.zones", *f.RefEntity)
	assert.Nil(t, f.RefDisplayField)

	broken := EntityDefinition{Name: "x.y", Label: "Y", TableName: "x.y", Fields: []EntityField{RefField("zoneId", "Zone", "waf.zones", "hostname")}}
	reg2 := NewMetadataRegistry()
	require.NoError(t, reg2.Register(broken))
	assert.Error(t, reg2.ValidateReferences(), "a real Reference to an unknown entity is still refused")
}
