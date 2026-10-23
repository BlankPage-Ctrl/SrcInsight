package methods

import (
	"context"
	"strings"
	"testing"

	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
)

// recallVault mirrors the shape that broke the old anchor pass: a two-domain
// repo where "discover"/"register" have both a dead exact wrapper and a live
// implementation cluster, plus unrelated bare-word matches (folder/manifest).
func recallVault(t *testing.T) *cold.Vault {
	t.Helper()
	vault, err := cold.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	ctx := context.Background()

	rows := []cold.EntryRow{
		{ID: "typescript:desktop/internal/plugins/service.go:method:Service.Discover:1", Kind: model.KindMethod, Name: "Discover", Qualified: "Service.Discover", FilePath: "desktop/internal/plugins/service.go", Dialect: model.LangGo, StartLine: 114, EndLine: 116, Signature: "func (s *Service) Discover()"},
		{ID: "typescript:desktop/internal/plugins/service.go:method:Service.Register:1", Kind: model.KindMethod, Name: "Register", Qualified: "Service.Register", FilePath: "desktop/internal/plugins/service.go", Dialect: model.LangGo, StartLine: 118, EndLine: 124, Signature: "func (s *Service) Register()"},
		{ID: "typescript:apps/actions/discover.plugin.ts:function:discoverPlugins:1", Kind: model.KindFunction, Name: "discoverPlugins", Qualified: "discoverPlugins", FilePath: "apps/actions/discover.plugin.ts", Dialect: model.LangTypeScript, StartLine: 10, EndLine: 30, Signature: "function discoverPlugins()"},
		{ID: "typescript:apps/actions/discover.plugin.ts:function:assertPluginsEnabled:1", Kind: model.KindFunction, Name: "assertPluginsEnabled", Qualified: "assertPluginsEnabled", FilePath: "apps/actions/discover.plugin.ts", Dialect: model.LangTypeScript, StartLine: 32, EndLine: 38, Signature: "function assertPluginsEnabled()"},
		{ID: "typescript:apps/plugins/external-registry.ts:function:registerPlugin:1", Kind: model.KindFunction, Name: "registerPlugin", Qualified: "registerPlugin", FilePath: "apps/plugins/external-registry.ts", Dialect: model.LangTypeScript, StartLine: 14, EndLine: 20, Signature: "function registerPlugin()"},
		{ID: "typescript:apps/plugins/external-registry.ts:function:pluginManifest:1", Kind: model.KindFunction, Name: "pluginManifest", Qualified: "pluginManifest", FilePath: "apps/plugins/external-registry.ts", Dialect: model.LangTypeScript, StartLine: 22, EndLine: 30, Signature: "function pluginManifest()"},
		{ID: "typescript:apps/plugins/external-registry.ts:const:PLUGINS_DIR:1", Kind: model.KindConst, Name: "PLUGINS_DIR", Qualified: "PLUGINS_DIR", FilePath: "apps/plugins/external-registry.ts", Dialect: model.LangTypeScript, StartLine: 5, EndLine: 5, Signature: "const PLUGINS_DIR"},
		{ID: "typescript:apps/actions/register.plugin.ts:function:registerPluginAction:1", Kind: model.KindFunction, Name: "registerPluginAction", Qualified: "registerPluginAction", FilePath: "apps/actions/register.plugin.ts", Dialect: model.LangTypeScript, StartLine: 10, EndLine: 20, Signature: "function registerPluginAction()"},
		{ID: "typescript:apps/routes/plugins.ts:function:pluginMethods:1", Kind: model.KindFunction, Name: "pluginMethods", Qualified: "pluginMethods", FilePath: "apps/routes/plugins.ts", Dialect: model.LangTypeScript, StartLine: 10, EndLine: 20, Signature: "function pluginMethods()"},
		{ID: "typescript:apps/routes/plugins.ts:function:registerPluginRoute:1", Kind: model.KindFunction, Name: "registerPluginRoute", Qualified: "registerPluginRoute", FilePath: "apps/routes/plugins.ts", Dialect: model.LangTypeScript, StartLine: 22, EndLine: 28, Signature: "function registerPluginRoute()"},
		{ID: "typescript:src/database/migrate.ts:const:folder:1", Kind: model.KindConst, Name: "folder", Qualified: "folder", FilePath: "src/database/migrate.ts", Dialect: model.LangTypeScript, StartLine: 29, EndLine: 29, Signature: "const folder"},
		{ID: "typescript:src/database/migrate.ts:function:resolveMigrationsFolder:1", Kind: model.KindFunction, Name: "resolveMigrationsFolder", Qualified: "resolveMigrationsFolder", FilePath: "src/database/migrate.ts", Dialect: model.LangTypeScript, StartLine: 31, EndLine: 35, Signature: "function resolveMigrationsFolder()"},
		{ID: "typescript:apps/insight/manifest.ts:const:manifest:1", Kind: model.KindConst, Name: "manifest", Qualified: "manifest", FilePath: "apps/insight/manifest.ts", Dialect: model.LangTypeScript, StartLine: 106, EndLine: 106, Signature: "const manifest"},
		{ID: "typescript:apps/insight/manifest.ts:function:loadInsightManifest:1", Kind: model.KindFunction, Name: "loadInsightManifest", Qualified: "loadInsightManifest", FilePath: "apps/insight/manifest.ts", Dialect: model.LangTypeScript, StartLine: 90, EndLine: 104, Signature: "function loadInsightManifest()"},
	}
	if _, err := vault.CatalogEntries(ctx, rows); err != nil {
		t.Fatal(err)
	}
	links := []cold.LinkRow{
		{Origin: "typescript:apps/routes/plugins.ts:function:pluginMethods:1", Target: "typescript:apps/actions/discover.plugin.ts:function:discoverPlugins:1", Kind: model.EdgeCalls},
		{Origin: "typescript:apps/routes/plugins.ts:function:pluginMethods:1", Target: "typescript:apps/plugins/external-registry.ts:function:registerPlugin:1", Kind: model.EdgeCalls},
		{Origin: "typescript:apps/actions/discover.plugin.ts:function:discoverPlugins:1", Target: "typescript:apps/actions/discover.plugin.ts:function:assertPluginsEnabled:1", Kind: model.EdgeCalls},
		{Origin: "typescript:apps/routes/plugins.ts:function:registerPluginRoute:1", Target: "typescript:apps/actions/register.plugin.ts:function:registerPluginAction:1", Kind: model.EdgeCalls},
	}
	if _, err := vault.CatalogLinks(ctx, links); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		"desktop/internal/plugins/service.go",
		"apps/actions/discover.plugin.ts",
		"apps/plugins/external-registry.ts",
		"apps/actions/register.plugin.ts",
		"apps/routes/plugins.ts",
		"src/database/migrate.ts",
		"apps/insight/manifest.ts",
	} {
		if err := vault.NoteOrigin(ctx, cold.OriginRecord{Path: f, Fingerprint: "test", Dialect: model.LangTypeScript}); err != nil {
			t.Fatal(err)
		}
	}
	return vault
}

func TestTracePrefersCorroboratedCallersOverExactWrapper(t *testing.T) {
	rep, err := Trace(context.Background(), recallVault(t), TraceAsk{
		Query:     "plugin discover register manifest folder",
		MaxTrails: 4,
	})
	if err != nil {
		t.Fatal(err)
	}

	discover := rep.Anchors["discover"]
	if len(discover) == 0 {
		t.Fatal("no anchors for discover")
	}
	if discover[0].Name != "discoverPlugins" {
		t.Fatalf("top discover anchor = %q, want discoverPlugins (anchors=%v)", discover[0].Name, anchorNames(discover))
	}

	for _, hit := range rep.Anchors["folder"] {
		if strings.Contains(hit.FilePath, "migrate") {
			t.Fatalf("uncorroborated folder match survived: %s", hit.FilePath)
		}
	}
	for _, hit := range rep.Anchors["manifest"] {
		if hit.FilePath != "apps/plugins/external-registry.ts" {
			t.Fatalf("manifest anchor %s %q, want only the plugin cluster", hit.FilePath, hit.Name)
		}
	}

	if len(rep.Trails) == 0 {
		t.Fatal("no trails across the plugin cluster")
	}
	found := false
	for _, tr := range rep.Trails {
		for i := 0; i+1 < len(tr.Steps); i++ {
			if tr.Steps[i] == "typescript:apps/actions/discover.plugin.ts:function:discoverPlugins:1" &&
				tr.Steps[i+1] == "typescript:apps/actions/discover.plugin.ts:function:assertPluginsEnabled:1" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("no discoverPlugins->assertPluginsEnabled trail in %v", rep.Trails)
	}
}

func anchorNames(hits []model.SearchHit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.Name+"@"+h.FilePath)
	}
	return out
}

func TestTraceDirectedFromToCaseInsensitive(t *testing.T) {
	rep, err := Trace(context.Background(), recallVault(t), TraceAsk{
		Query:     "pluginMethods discoverPlugins",
		From:      "PLUGINMETHODS",
		To:        "DISCOVERPLUGINS",
		MaxTrails: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Trails) == 0 {
		t.Fatal("no directed trail; From/To lookup dropped the anchors")
	}
	steps := rep.Trails[0].Steps
	if len(steps) != 2 ||
		steps[0] != "typescript:apps/routes/plugins.ts:function:pluginMethods:1" ||
		steps[1] != "typescript:apps/actions/discover.plugin.ts:function:discoverPlugins:1" {
		t.Fatalf("directed steps = %v", steps)
	}
}
