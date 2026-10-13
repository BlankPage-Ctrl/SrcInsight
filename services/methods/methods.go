// Package methods owns the RPC method surface; one file per method.
package methods

import (
	"srcinsight/internal/storage/cold"
	"srcinsight/transport/stdio"
)

type Service struct {
	vault *cold.Vault
}

func New(vault *cold.Vault) *Service {
	return &Service{vault: vault}
}

func MustRegister(srv *stdio.Server, name string, h stdio.Handler) {
	if !srv.Register(name, h) {
		panic("methods: cannot register " + name)
	}
}
