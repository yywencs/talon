package simulator

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/scenario"
)

func TestNewWorldBuildsEveryVersionOneScenario(t *testing.T) {
	dataset := loadTestDataset(t)
	for _, item := range dataset.Cases {
		t.Run(item.Scenario.Metadata.ID, func(t *testing.T) {
			world, err := NewWorld(item.Scenario)
			require.NoError(t, err)
			require.Equal(t, item.Scenario.Metadata.ID, world.Snapshot().ScenarioID)
		})
	}
}

func TestNewWorldBuildsMappingBaseline(t *testing.T) {
	item := findTestCase(t, "mapping-regression-rollback-001")
	world, err := NewWorld(item.Scenario)
	require.NoError(t, err)

	snapshot := world.Snapshot()
	require.Equal(t, time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC), snapshot.Now)
	require.Equal(t, time.Minute, snapshot.Tick)
	require.Equal(t, snapshot.Now.Add(40*time.Minute), snapshot.EndAt)
	require.Equal(t, 80, snapshot.Routes["route-a"].Weight)
	require.Equal(t, 80, snapshot.Routes["route-a"].BaselineWeight)
	require.Equal(t, platform.ProviderHealthy, snapshot.Providers["provider-a"].Health)
	require.True(t, snapshot.Configs["mapping-v1"].Active)
	require.True(t, snapshot.Configs["mapping-v1"].KnownHealthy)
	require.Equal(t, 100, snapshot.Traffic.RequestsPerMinute)
	require.Len(t, snapshot.Metrics, 4)

	tool := snapshot.Services["image-service"].Tools["generate_image"]
	require.Equal(t, []string{"route-a", "route-b"}, tool.RouteIDs)
	require.Equal(t, 0.98, tool.SLO.SuccessRateMin)
	require.Equal(t, int64(3000), tool.SLO.LatencyP95MSMax)
}

func TestSnapshotDoesNotExposeMutableWorldState(t *testing.T) {
	item := findTestCase(t, "mapping-regression-rollback-001")
	world, err := NewWorld(item.Scenario)
	require.NoError(t, err)

	first := world.Snapshot()
	route := first.Routes["route-a"]
	route.Weight = 0
	first.Routes["route-a"] = route
	service := first.Services["image-service"]
	tool := service.Tools["generate_image"]
	tool.RouteIDs[0] = "modified-route"
	service.Tools["generate_image"] = tool
	first.Services["image-service"] = service
	first.Metrics[0].Dimensions["service_id"] = "modified-service"
	compatible := first.Providers["provider-a"].SchemaCompatible
	require.NotNil(t, compatible)
	*compatible = false

	second := world.Snapshot()
	require.Equal(t, 80, second.Routes["route-a"].Weight)
	require.Equal(t, "route-a", second.Services["image-service"].Tools["generate_image"].RouteIDs[0])
	require.Equal(t, "image-service", second.Metrics[0].Dimensions["service_id"])
	require.True(t, *second.Providers["provider-a"].SchemaCompatible)
}

func TestNewWorldKeepsCredentialSecretsOutOfRuntimeState(t *testing.T) {
	item := findTestCase(t, "credential-revoked-escalation-001")
	world, err := NewWorld(item.Scenario)
	require.NoError(t, err)

	credential := world.Snapshot().Credentials["provider-doc-a"]
	require.Equal(t, "credential-doc-a-v7", credential.CredentialID)
	encoded, err := json.Marshal(credential)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret")

	document := item.Scenario
	metadata := *document.InitialState.CredentialMetadata
	metadata.SecretVisible = true
	document.InitialState.CredentialMetadata = &metadata
	_, err = NewWorld(document)
	require.ErrorContains(t, err, "must not expose secret material")
}

func TestNewWorldRejectsRouteWithUnknownProvider(t *testing.T) {
	item := findTestCase(t, "mapping-regression-rollback-001")
	document := item.Scenario
	document.InitialState.Service.Routes = append(
		[]scenario.InitialRoute(nil),
		document.InitialState.Service.Routes...,
	)
	document.InitialState.Service.Routes[0].Provider = "missing-provider"

	_, err := NewWorld(document)
	require.ErrorContains(t, err, "references unknown provider")
}

func loadTestDataset(t *testing.T) *scenario.Dataset {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "data", "toolops-v1"))
	dataset, err := scenario.LoadDataset(root)
	require.NoError(t, err)
	return dataset
}

func findTestCase(t *testing.T, id string) *scenario.Case {
	t.Helper()
	item, ok := loadTestDataset(t).Find(id)
	require.True(t, ok)
	return item
}
