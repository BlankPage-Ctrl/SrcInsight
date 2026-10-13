package typescript

import (
	"testing"
)

func TestExtractReExports(t *testing.T) {
	src := `import * as z from "zod";
import { C as D } from './types';
export * from "./core/index.js";
export { string, _parse as parse } from "./core/parse.js";
export { local };
export const x = 1;
`
	fe := extractTS(t, src)
	byLocal := map[string]string{}
	for _, im := range fe.Imports {
		byLocal[im.LocalName] = im.Source + "|" + im.Kind
	}
	// Namespace keeps its name for probing, "*" alias for star fallback.
	if byLocal["z"] != "zod|namespace" {
		t.Errorf("z = %q, want zod|namespace", byLocal["z"])
	}
	star := false
	for _, im := range fe.Imports {
		if im.Alias == "*" && im.Source == "zod" {
			star = true
		}
	}
	if !star {
		t.Errorf("namespace import must carry Alias *, got %+v", fe.Imports)
	}
	if byLocal["D"] != "./types|named" {
		t.Errorf("D = %q, want ./types|named", byLocal["D"])
	}
	// Re-exports ride the import machinery.
	if byLocal["string"] != "./core/parse.js|re-export" {
		t.Errorf("string = %q, want re-export", byLocal["string"])
	}
	if byLocal["parse"] != "./core/parse.js|re-export" {
		t.Errorf("parse = %q, want re-export under alias", byLocal["parse"])
	}
	for _, im := range fe.Imports {
		if im.LocalName == "local" {
			if im.Kind != "export-alias" || im.Source != "" {
				t.Errorf("plain export {local} must be export-alias without source: %+v", im)
			}
		}
	}
}
