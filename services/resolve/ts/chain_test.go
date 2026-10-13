package ts

import (
	"testing"

	"srcinsight/internal/model"
)

func TestSplitThisChain(t *testing.T) {
	cases := []struct {
		in          string
		field, meth string
		ok          bool
	}{
		{"this.svc.findAll", "svc", "findAll", true},
		{"self.svc.findAll", "svc", "findAll", true},
		{"this.a.b.c", "a", "c", true},
		{"this.help", "", "", false},
		{"self.help", "", "", false},
		{"svc.findAll", "", "", false},
		{"findAll", "", "", false},
		{"this..findAll", "", "", false},
		{"this.svc.", "", "", false},
	}
	for _, c := range cases {
		f, m, ok := SplitThisChain(c.in)
		if f != c.field || m != c.meth || ok != c.ok {
			t.Errorf("SplitThisChain(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.in, f, m, ok, c.field, c.meth, c.ok)
		}
	}
}

func TestDIChainTargetsTyped(t *testing.T) {
	ctor := &model.Function{
		FunctionRef: model.FunctionRef{ID: "ctor", Name: "constructor", FilePath: "f.ts"},
		Kind:        model.KindConstructor,
		Container:   &model.Container{Kind: model.ContainerClass, Name: "C"},
		RecvHints:   []model.RecvBinding{{Recv: "svc", Type: "Svc"}},
	}
	caller := &model.Function{
		FunctionRef: model.FunctionRef{ID: "caller", Name: "findAll", FilePath: "f.ts"},
		Kind:        model.KindMethod,
		Container:   &model.Container{Kind: model.ContainerClass, Name: "C"},
	}
	target := &model.Function{
		FunctionRef: model.FunctionRef{ID: "target", Name: "findAll", FilePath: "f.ts"},
		Kind:        model.KindMethod,
		Container:   &model.Container{Kind: model.ContainerClass, Name: "Svc"},
	}
	byName := map[string][]*model.Function{"findAll": {caller, target}}
	binds := BuildCtorBinds([]*model.Function{ctor, caller, target})
	got := DIChainTargets(caller, byName, binds, "svc", "findAll")
	if len(got) != 1 || got[0].ID != "target" {
		t.Fatalf("DIChainTargets = %+v, want [target]", got)
	}
	if got := DIChainTargets(caller, byName, binds, "missing", "findAll"); len(got) != 0 {
		t.Fatalf("DIChainTargets unknown field = %+v, want none", got)
	}
}

func TestRecvGateModes(t *testing.T) {
	t.Setenv("SRCINSIGHT_RECV", "0")
	if RecvForwardEnabled() || RecvReverseEnabled() {
		t.Fatal("mode 0: want both off")
	}
	t.Setenv("SRCINSIGHT_RECV", "forward")
	if !RecvForwardEnabled() || RecvReverseEnabled() {
		t.Fatal("mode forward: want forward on, reverse off")
	}
	t.Setenv("SRCINSIGHT_RECV", "")
	if !RecvForwardEnabled() || !RecvReverseEnabled() {
		t.Fatal("mode unset: want both on")
	}
}
