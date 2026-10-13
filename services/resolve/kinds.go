package resolve

import (
	"strings"

	"srcinsight/internal/model"
)

// IsTypeKind reports heritage-linkable kinds.
func IsTypeKind(k model.Kind) bool {
	switch k {
	case model.KindClassDef, model.KindInterface, model.KindTypeAlias, model.KindEnum:
		return true
	default:
		return false
	}
}

// IsCallableKind reports call-target kinds; consts may receive calls.
func IsCallableKind(k model.Kind) bool {
	switch k {
	case model.KindClassDef, model.KindInterface, model.KindTypeAlias, model.KindEnum, model.KindModule:
		return false
	default:
		return true
	}
}

// SplitCrossRecv splits recv.simple; plain names yield empty recv.
func SplitCrossRecv(name string) (recv, simple string) {
	if i := strings.Index(name, "."); i >= 0 {
		recv = name[:i]
		simple = name[i+1:]
		if j := strings.LastIndex(simple, "."); j >= 0 {
			simple = simple[j+1:]
		}
		return recv, simple
	}
	return "", name
}
