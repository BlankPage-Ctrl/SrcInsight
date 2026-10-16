package services

import (
	"strings"

	"srcinsight/internal/convention"
	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
)

// transportRows resolves extract hints to vault rows. Owners come from the
// indexed functions of the same file: qualified name first, plain name as
// fallback (Vue attach happens after hint capture). Express route markers
// riding the Calls slice become endpoint rows instead of call rows.
func transportRows(fe model.FileExtract, fns []*model.Function) ([]cold.TransportEndpointRow, []cold.TransportCallRow) {
	byQualified := make(map[string]model.EntryID, len(fns))
	byName := make(map[string]model.EntryID, len(fns))
	for _, f := range fns {
		if f.ID == "" {
			continue
		}
		if _, ok := byQualified[f.QualifiedName()]; !ok {
			byQualified[f.QualifiedName()] = f.ID
		}
		if _, ok := byName[f.Name]; !ok {
			byName[f.Name] = f.ID
		}
	}
	ownerOf := func(fn string) model.EntryID {
		if id, ok := byQualified[fn]; ok {
			return id
		}
		if i := strings.LastIndex(fn, "."); i >= 0 {
			if id, ok := byName[fn[i+1:]]; ok {
				return id
			}
		}
		if id, ok := byName[fn]; ok {
			return id
		}
		return ""
	}
	var endpoints []cold.TransportEndpointRow
	for _, e := range fe.Endpoints {
		endpoints = append(endpoints, cold.TransportEndpointRow{
			FilePath:  fe.Path,
			Owner:     ownerOf(e.Func),
			Transport: e.Transport,
			Method:    convention.NormalizeMethod(e.Method),
			Path:      convention.NormalizePath(e.Path),
			Operation: e.Operation,
			Framework: e.Framework,
			SiteStart: e.Line,
		})
	}
	var calls []cold.TransportCallRow
	for _, c := range fe.Calls {
		if c.Lib == "express-route" {
			path := strings.TrimPrefix(c.URL, "__endpoint__:")
			endpoints = append(endpoints, cold.TransportEndpointRow{
				FilePath:  fe.Path,
				Owner:     ownerOf(c.Func),
				Transport: c.Transport,
				Method:    convention.NormalizeMethod(c.Method),
				Path:      convention.NormalizePath(path),
				Framework: "express",
				SiteStart: c.Line,
			})
			continue
		}
		calls = append(calls, cold.TransportCallRow{
			FilePath:  fe.Path,
			Owner:     ownerOf(c.Func),
			Transport: c.Transport,
			Method:    convention.NormalizeMethod(c.Method),
			URL:       convention.StripURL(c.URL),
			Operation: c.Operation,
			Lib:       c.Lib,
			SiteStart: c.Line,
		})
	}
	return endpoints, calls
}
