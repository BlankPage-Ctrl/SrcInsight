// Package methods owns the RPC method surface; one file per method.
package methods

import (
	"srcinsight/internal/storage/cold"
	"srcinsight/transport/stdio"
)

type Service struct {
	vault *cold.Vault
	live  LiveScanner
}

// Option tunes a Service at construction; nil options are ignored.
type Option func(*Service)

// WithLive attaches the live reference scanner refs uses to confirm loose
// call sites through imports. Without it refs answers from the vault only.
func WithLive(l LiveScanner) Option {
	return func(s *Service) {
		if l != nil {
			s.live = l
		}
	}
}

func New(vault *cold.Vault, opts ...Option) *Service {
	s := &Service{vault: vault}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

func MustRegister(srv *stdio.Server, name string, h stdio.Handler) {
	if !srv.Register(name, h) {
		panic("methods: cannot register " + name)
	}
}
