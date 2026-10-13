package typescript

import (
	"testing"

	"srcinsight/internal/model"
)

func TestCollectRecvBindings(t *testing.T) {
	src := `
import { z } from './core';
import * as core from '../core/index.js';

class ZodString { safeParse(v: unknown): boolean { return true; } }

const direct = new ZodString();
let annotated: ZodString;
const casted = make() as ZodString;
const factory = z.string();
const dotted = core.make();
const lit = 42;
const anon = () => 1;
const { a } = obj;

function use(param: ZodString, other: string, ...rest: ZodString[]) {
  return param.safeParse(other);
}
`
	fe := extractTS(t, src)
	got := map[string]string{}
	for _, b := range fe.RecvBindings {
		if b.ViaCall != "" {
			got[b.Recv] = "call:" + b.ViaCall
		} else {
			got[b.Recv] = "type:" + b.Type
		}
	}
	want := map[string]string{
		"direct":    "type:ZodString",
		"annotated": "type:ZodString",
		"casted":    "type:ZodString",
		"factory":   "call:z.string",
		"dotted":    "call:core.make",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("bind %q = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	for _, k := range []string{"lit", "anon", "a"} {
		if _, ok := got[k]; ok {
			t.Errorf("bind %q should be absent, got %v", k, got)
		}
	}

	var useFn *model.Function
	for i := range fe.Functions {
		if fe.Functions[i].Name == "use" {
			useFn = &fe.Functions[i]
		}
	}
	if useFn == nil {
		t.Fatal("use missing")
	}
	hints := map[string]string{}
	for _, h := range useFn.RecvHints {
		hints[h.Recv] = h.Type
	}
	if hints["param"] != "ZodString" {
		t.Errorf("param hint = %v, want ZodString", hints)
	}
	if _, ok := hints["other"]; ok {
		t.Errorf("primitive param must not bind: %v", hints)
	}
	if hints["rest"] != "ZodString" {
		t.Errorf("rest param hint = %v, want ZodString", hints)
	}
}

func TestArrowValuedReturnAndParams(t *testing.T) {
	src := `
export class ZodObject {
  static create = (params?: { name?: string }): ZodObject => {
    return null as unknown as ZodObject;
  };
}
export function plain(x: number): number { return x; }
`
	fe := extractTS(t, src)
	byName := map[string]model.Function{}
	for _, f := range fe.Functions {
		byName[f.Name] = f
	}
	create, ok := byName["create"]
	if !ok {
		t.Fatalf("create missing; got %v", funcNames(fe))
	}
	if len(create.ReturnTypes) != 1 || create.ReturnTypes[0].Name != "ZodObject" {
		t.Errorf("create returns = %+v, want [ZodObject]", create.ReturnTypes)
	}
	if len(create.ParamTypes) != 0 {
		t.Errorf("create params = %+v, want none (all primitives/structs)", create.ParamTypes)
	}
}
