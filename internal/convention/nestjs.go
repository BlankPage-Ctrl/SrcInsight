package convention

import "strings"

// NestJS scope covers REST class identity plus route verbs and GraphQL resolver operations.
// Model and param decorators stay out: constructor DI already rides param edges and models are not entry points.
var (
	NestClassDecors = []string{
		"Controller",
		"Injectable",
		"Module",
	}
	NestMethodDecors = []string{
		"Get",
		"Post",
		"Put",
		"Delete",
		"Patch",
		"Options",
		"Head",
		"All",
	}
	// NestGQLClassDecors marks GraphQL resolver classes.
	NestGQLClassDecors = []string{
		"Resolver",
	}
	// NestGQLMethodDecors marks GraphQL operation methods: entry points
	// like HTTP routes, but nouns rather than verbs.
	NestGQLMethodDecors = []string{
		"Query",
		"Mutation",
		"Subscription",
		"ResolveField",
	}
	// NestFileSuffixes marks NestJS layout files by name.
	NestFileSuffixes = []string{
		".controller.ts",
		".service.ts",
		".module.ts",
		".resolver.ts",
	}
)

var nestClassSet = map[string]struct{}{
	"Controller": {}, "Injectable": {}, "Module": {},
}

var nestMethodSet = map[string]struct{}{
	"Get": {}, "Post": {}, "Put": {}, "Delete": {},
	"Patch": {}, "Options": {}, "Head": {}, "All": {},
}

var nestGQLClassSet = map[string]struct{}{
	"Resolver": {},
}

var nestGQLMethodSet = map[string]struct{}{
	"Query": {}, "Mutation": {}, "Subscription": {}, "ResolveField": {},
}

// nestGQLMethodTags maps GraphQL decorators to Entry tags; ResolveField keeps its qualifier vs Field.
var nestGQLMethodTags = map[string]string{
	"Query":        "query",
	"Mutation":     "mutation",
	"Subscription": "subscription",
	"ResolveField": "resolve-field",
}

func init() {
	Register(Framework{
		Name:         "nestjs",
		FileSuffixes: append([]string(nil), NestFileSuffixes...),
		ClassDecors:  append(append([]string(nil), NestClassDecors...), NestGQLClassDecors...),
		MethodDecors: append(append([]string(nil), NestMethodDecors...), NestGQLMethodDecors...),
	})
}

// IsNestClassDecor reports whether name is a v1 NestJS class decorator.
func IsNestClassDecor(name string) bool {
	_, ok := nestClassSet[name]
	return ok
}

// IsNestMethodDecor reports whether name is a v1 NestJS route decorator.
func IsNestMethodDecor(name string) bool {
	_, ok := nestMethodSet[name]
	return ok
}

// IsNestDecor reports whether name is any known NestJS decorator,
// REST or GraphQL.
func IsNestDecor(name string) bool {
	return IsNestClassDecor(name) || IsNestMethodDecor(name) || IsNestGQLDecor(name)
}

// IsNestGQLClassDecor reports whether name is a GraphQL resolver class
// decorator (@Resolver).
func IsNestGQLClassDecor(name string) bool {
	_, ok := nestGQLClassSet[name]
	return ok
}

// IsNestGQLMethodDecor reports whether name is a GraphQL operation method
// decorator (@Query/@Mutation/@Subscription/@ResolveField).
func IsNestGQLMethodDecor(name string) bool {
	_, ok := nestGQLMethodSet[name]
	return ok
}

// IsNestGQLDecor reports whether name is any known GraphQL decorator.
func IsNestGQLDecor(name string) bool {
	return IsNestGQLClassDecor(name) || IsNestGQLMethodDecor(name)
}

// IsNestFile reports NestJS layout files by suffix.
func IsNestFile(p string) bool {
	lower := strings.ToLower(p)
	for _, suf := range NestFileSuffixes {
		if strings.HasSuffix(lower, suf) {
			return true
		}
	}
	return false
}

// NestTag normalizes a decorator into its Entry tag: verbs keep a route prefix, GraphQL maps to nouns.
// The GQL lookup precedes lowering so a future verb/noun collision stays loud.
func NestTag(name string) string {
	if IsNestMethodDecor(name) {
		return "route:" + name
	}
	if tag, ok := nestGQLMethodTags[name]; ok {
		return tag
	}
	return strings.ToLower(name)
}
