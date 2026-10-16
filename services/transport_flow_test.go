package services

import (
	"context"
	"testing"

	"srcinsight/services/methods"
)

const transportControllerSrc = `import { Controller, Get } from "@nestjs/common";
@Controller("cats")
export class CatsController {
  @Get(":id")
  findOne() { return 1; }
}
`

const transportClientSrc = `export async function loadCat(id: string) {
  const r = await fetch("/cats/" + id);
  return r;
}
`

func TestTransportFlowEndToEnd(t *testing.T) {
	ctx := context.Background()
	p, _ := testPipeline(t, map[string]string{
		"src/cats.controller.ts": transportControllerSrc,
		"src/loader.ts":          transportClientSrc,
	})
	defer p.Close()
	if _, err := p.FullScan(ctx, true); err != nil {
		t.Fatal(err)
	}
	rep, err := methods.Transport(ctx, p.Vault(), methods.TransportAsk{Method: "GET", Path: "/cats/42"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Flows) != 1 {
		t.Fatalf("flows = %+v, want 1", rep.Flows)
	}
	f := rep.Flows[0]
	if f.Call.Lib != "bare-fetch" {
		t.Fatalf("call lib = %q, want bare-fetch", f.Call.Lib)
	}
	if f.Route.Framework != "nestjs" {
		t.Fatalf("route framework = %q, want nestjs", f.Route.Framework)
	}
	if len(rep.Edges) != 1 {
		t.Fatalf("edges = %+v, want 1", rep.Edges)
	}
}
