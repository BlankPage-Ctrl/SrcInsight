package transport

import "srcinsight/internal/model"

type Kind string

const (
	KindHTTP    Kind = "http"
	KindGraphQL Kind = "graphql"
	KindWS      Kind = "ws"
)

type Endpoint struct {
	Transport Kind          `json:"transport"`
	Method    string        `json:"method"`
	Path      string        `json:"path"`
	Operation string        `json:"operation"`
	Framework string        `json:"framework"`
	Owner     model.EntryID `json:"owner"`
}

type CallSite struct {
	Transport Kind          `json:"transport"`
	Method    string        `json:"method"`
	URL       string        `json:"url"`
	Operation string        `json:"operation"`
	Lib       string        `json:"lib"`
	Owner     model.EntryID `json:"owner"`
	MethodSet bool          `json:"methodSet"`
}
