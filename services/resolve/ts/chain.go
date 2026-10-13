package ts

import (
	"strings"

	"srcinsight/internal/model"
)

// SplitThisChain parses this.field.rest into field and method; bare this.m fails.
func SplitThisChain(name string) (field, method string, ok bool) {
	var rest string
	switch {
	case strings.HasPrefix(name, "this."):
		rest = name[len("this."):]
	case strings.HasPrefix(name, "self."):
		rest = name[len("self."):]
	default:
		return "", "", false
	}
	i := strings.Index(rest, ".")
	if i <= 0 {
		return "", "", false
	}
	field = rest[:i]
	method = rest[i+1:]
	if j := strings.LastIndex(method, "."); j >= 0 {
		method = method[j+1:]
	}
	if field == "" || method == "" {
		return "", "", false
	}
	return field, method, true
}

// BuildCtorBinds indexes constructor parameter-property types per class.
func BuildCtorBinds(fns []*model.Function) map[string]map[string]string {
	ctorBinds := make(map[string]map[string]string, len(fns))
	for _, fn := range fns {
		if fn == nil || fn.Kind != model.KindConstructor || fn.ContainerName() == "" {
			continue
		}
		m := ctorBinds[fn.ContainerName()]
		if m == nil {
			m = make(map[string]string)
			ctorBinds[fn.ContainerName()] = m
		}
		for _, h := range fn.RecvHints {
			if h.Recv != "" && h.Type != "" {
				if _, ok := m[h.Recv]; !ok {
					m[h.Recv] = h.Type
				}
			}
		}
	}
	return ctorBinds
}

// DIChainTargets resolves this.field.method via the constructor-declared field type.
func DIChainTargets(caller *model.Function, byName map[string][]*model.Function, ctorBinds map[string]map[string]string, field, method string) []*model.Function {
	cls := caller.ContainerName()
	if cls == "" {
		return nil
	}
	typ, ok := ctorBinds[cls][field]
	if !ok || typ == "" {
		return nil
	}
	var out []*model.Function
	for _, t := range byName[method] {
		if t.ContainerName() == typ {
			out = append(out, t)
		}
	}
	return out
}
