package reconciler

import (
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/metap-org/metap-go/metadata"
)

func orchClaimed(tenant uuid.UUID, entityName string) ClaimedEntity {
	return ClaimedEntity{TenantID: tenant, EntityName: entityName, DesiredVersion: 1}
}

func orchDef(name string, fields []metadata.EntityField) metadata.EntityDefinition {
	return metadata.EntityDefinition{
		Name: name, Label: name, TableName: "entities." + strings.ReplaceAll(name, ".", "_"), Fields: fields,
	}
}

func orchReferenceField(name, refEntity string) metadata.EntityField {
	f := plainField(name, metadata.KindReference)
	f.RefEntity = &refEntity
	return f
}

func waveNames(waves [][]ClaimedEntity) [][]string {
	out := make([][]string, len(waves))
	for i, wave := range waves {
		names := make([]string, len(wave))
		for j, e := range wave {
			names[j] = e.EntityName
		}
		sort.Strings(names)
		out[i] = names
	}
	return out
}

func assertWaveNames(t *testing.T, got [][]string, want [][]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	}
}

func TestTopoSortOrdersReferencedEntityBeforeReferencer(t *testing.T) {
	tenant := uuid.New()
	sprints := orchDef("jira.sprints", []metadata.EntityField{plainField("name", metadata.KindString)})
	issues := orchDef("jira.issues", []metadata.EntityField{orchReferenceField("sprint", "jira.sprints")})
	waves := TopoSortWaves([]ClaimedPair{
		{orchClaimed(tenant, "jira.issues"), issues},
		{orchClaimed(tenant, "jira.sprints"), sprints},
	})
	assertWaveNames(t, waveNames(waves), [][]string{{"jira.sprints"}, {"jira.issues"}})
}

func TestTopoSortPutsIndependentEntitiesInTheSameWave(t *testing.T) {
	tenant := uuid.New()
	a := orchDef("a", []metadata.EntityField{plainField("name", metadata.KindString)})
	b := orchDef("b", []metadata.EntityField{plainField("name", metadata.KindString)})
	waves := TopoSortWaves([]ClaimedPair{{orchClaimed(tenant, "a"), a}, {orchClaimed(tenant, "b"), b}})
	assertWaveNames(t, waveNames(waves), [][]string{{"a", "b"}})
}

func TestTopoSortIgnoresReferenceToAnEntityOutsideTheBatch(t *testing.T) {
	tenant := uuid.New()
	issues := orchDef("jira.issues", []metadata.EntityField{orchReferenceField("project", "jira.projects")})
	waves := TopoSortWaves([]ClaimedPair{{orchClaimed(tenant, "jira.issues"), issues}})
	assertWaveNames(t, waveNames(waves), [][]string{{"jira.issues"}})
}

func TestTopoSortDoesNotOrderAcrossDifferentTenants(t *testing.T) {
	tenantA, tenantB := uuid.New(), uuid.New()
	sprints := orchDef("jira.sprints", []metadata.EntityField{plainField("name", metadata.KindString)})
	issues := orchDef("jira.issues", []metadata.EntityField{orchReferenceField("sprint", "jira.sprints")})
	waves := TopoSortWaves([]ClaimedPair{
		{orchClaimed(tenantA, "jira.issues"), issues},
		{orchClaimed(tenantA, "jira.sprints"), sprints},
		{orchClaimed(tenantB, "jira.issues"), issues},
	})
	if len(waves) != 2 {
		t.Fatalf("expected 2 waves, got %d", len(waves))
	}
	if len(waves[0]) != 2 {
		t.Fatalf("expected wave 0 to have 2 entities (tenant A's sprints + tenant B's issues), got %d", len(waves[0]))
	}
	if len(waves[1]) != 1 {
		t.Fatalf("expected wave 1 to have 1 entity (tenant A's issues), got %d", len(waves[1]))
	}
}

func TestTopoSortFallsBackToOneWaveOnADependencyCycle(t *testing.T) {
	tenant := uuid.New()
	a := orchDef("a", []metadata.EntityField{orchReferenceField("b_ref", "b")})
	b := orchDef("b", []metadata.EntityField{orchReferenceField("a_ref", "a")})
	waves := TopoSortWaves([]ClaimedPair{{orchClaimed(tenant, "a"), a}, {orchClaimed(tenant, "b"), b}})
	assertWaveNames(t, waveNames(waves), [][]string{{"a", "b"}})
}

func TestWaveSizeIsCanaryThenPercentagesCappedAtTotal(t *testing.T) {
	cases := []struct {
		total int
		wave  uint32
		want  int
	}{
		{0, 0, 0}, {1, 0, 1}, {1, 3, 1},
		{100, 0, 2}, {100, 1, 5}, {100, 2, 25}, {100, 3, 100},
		{10, 1, 2}, {10, 2, 3},
	}
	for _, c := range cases {
		if got := WaveSize(c.total, c.wave); got != c.want {
			t.Fatalf("WaveSize(%d, %d) = %d, want %d", c.total, c.wave, got, c.want)
		}
	}
}

func TestWaveSizeNeverExceedsTotal(t *testing.T) {
	for _, total := range []int{0, 1, 2, 3, 7, 40} {
		for wave := uint32(0); wave < 5; wave++ {
			if got := WaveSize(total, wave); got > total {
				t.Fatalf("WaveSize(%d, %d) = %d > total", total, wave, got)
			}
		}
	}
}
