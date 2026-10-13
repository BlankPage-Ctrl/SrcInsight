package typescript

import (
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

func extractNestTags(t *testing.T, path, src string) map[string][]string {
	t.Helper()
	parser := sitter.NewParser()
	defer parser.Close()
	setPathLanguage(t, parser, path)
	tree := parser.Parse([]byte(src), nil)
	if tree == nil {
		t.Fatal("parse failed")
	}
	defer tree.Close()
	fe, err := Extract(path, []byte(src), tree)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, f := range fe.Functions {
		out[f.QualifiedName()] = append([]string(nil), f.Tags...)
	}
	return out
}

func TestNestDecoratorTags(t *testing.T) {
	src := `import { Controller, Get, Injectable, Module } from "@nestjs/common";
@Injectable()
export class CatsService {
  findAll() { return []; }
}
@Controller("cats")
export class CatsController {
  constructor(private svc: CatsService) {}
  @Get()
  findAll() { return this.svc.findAll(); }
  @Post()
  @Get()
  doubled() { return 1; }
  plain() { return 2; }
}
@Module({ controllers: [CatsController], providers: [CatsService] })
export class CatsModule {}
`
	got := extractNestTags(t, "src/cats.controller.ts", src)
	if len(got["CatsService"]) != 1 || got["CatsService"][0] != "injectable" {
		t.Fatalf("CatsService tags = %v", got["CatsService"])
	}
	if len(got["CatsController"]) != 1 || got["CatsController"][0] != "controller" {
		t.Fatalf("CatsController tags = %v", got["CatsController"])
	}
	if len(got["CatsController.findAll"]) != 1 || got["CatsController.findAll"][0] != "route:Get" {
		t.Fatalf("findAll tags = %v", got["CatsController.findAll"])
	}
	if len(got["CatsController.doubled"]) != 2 {
		t.Fatalf("doubled tags = %v, want stacked [route:Post route:Get]", got["CatsController.doubled"])
	}
	if len(got["CatsController.plain"]) != 0 {
		t.Fatalf("plain tags = %v, want empty", got["CatsController.plain"])
	}
	if len(got["CatsModule"]) != 1 || got["CatsModule"][0] != "module" {
		t.Fatalf("CatsModule tags = %v", got["CatsModule"])
	}
	// Earlier method decorators must not leak onto later methods.
	if len(got["CatsService.findAll"]) != 0 {
		t.Fatalf("service findAll tags = %v, want empty", got["CatsService.findAll"])
	}
}

func TestUnknownDecoratorIgnored(t *testing.T) {
	src := `@Component({ selector: "x" })
export class X {}
`
	got := extractNestTags(t, "src/x.ts", src)
	if len(got["X"]) != 0 {
		t.Fatalf("X tags = %v, want empty (non-Nest decor)", got["X"])
	}
}

func TestGraphQLDecoratorTags(t *testing.T) {
	src := `import { Resolver, Query, Mutation, Subscription, ResolveField, Args } from "@nestjs/graphql";
import { UseGuards } from "@nestjs/common";
@Resolver(() => Shortcode)
export class ShortcodeResolver {
  constructor(private readonly shortcodeService: ShortcodeService) {}
  @Query(() => Shortcode, { nullable: true })
  async shortcode(@Args("code") code: string) { return null; }
  @UseGuards(GqlAuthGuard)
  @Mutation(() => Shortcode)
  async createShortcode(@Args("request") request: string) { return null; }
  @Subscription(() => Shortcode)
  myShortcodesCreated() { return null; }
  @ResolveField(() => String)
  owner() { return ""; }
  plainHelper() { return 1; }
}
`
	got := extractNestTags(t, "src/shortcode.resolver.ts", src)
	if len(got["ShortcodeResolver"]) != 1 || got["ShortcodeResolver"][0] != "resolver" {
		t.Fatalf("ShortcodeResolver tags = %v, want [resolver]", got["ShortcodeResolver"])
	}
	want := map[string]string{
		"ShortcodeResolver.shortcode":           "query",
		"ShortcodeResolver.createShortcode":     "mutation",
		"ShortcodeResolver.myShortcodesCreated": "subscription",
		"ShortcodeResolver.owner":               "resolve-field",
	}
	for qual, tag := range want {
		if len(got[qual]) != 1 || got[qual][0] != tag {
			t.Fatalf("%s tags = %v, want [%s]", qual, got[qual], tag)
		}
	}
	if len(got["ShortcodeResolver.plainHelper"]) != 0 {
		t.Fatalf("plainHelper tags = %v, want empty", got["ShortcodeResolver.plainHelper"])
	}
}

func TestRestQueryParamDecoratorUntagged(t *testing.T) {
	// @Query from @nestjs/common is a *parameter* decorator: it must never
	// leak onto the enclosing method as a GraphQL tag.
	src := `import { Controller, Post, Body, Query } from '@nestjs/common';
@Controller('auth')
export class AuthController {
  @Post('signin')
  async signInMagicLink(@Body() authData: string, @Query('origin') origin: string) {
    return origin;
  }
}
`
	got := extractNestTags(t, "src/auth.controller.ts", src)
	if len(got["AuthController"]) != 1 || got["AuthController"][0] != "controller" {
		t.Fatalf("AuthController tags = %v, want [controller]", got["AuthController"])
	}
	tags := got["AuthController.signInMagicLink"]
	if len(tags) != 1 || tags[0] != "route:Post" {
		t.Fatalf("signInMagicLink tags = %v, want [route:Post] (param @Query must not leak)", tags)
	}
}
