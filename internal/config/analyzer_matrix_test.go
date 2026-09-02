package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTypedAnalyzerDynamicFrameworkMatrix(t *testing.T) {
	tests := []struct {
		name       string
		framework  string
		runtime    string
		packageJSON string
		lockfile   string
		sourceName string
		source     string
	}{
		{name: "fastapi", framework: "fastapi", runtime: "asgi-uvicorn", sourceName: "app.py", source: "from fastapi import FastAPI\napp = FastAPI()\n", lockfile: "uv.lock"},
		{name: "starlette", framework: "fastapi", runtime: "asgi-uvicorn", sourceName: "app.py", source: "from starlette.applications import Starlette\napp = Starlette()\n", lockfile: "uv.lock"},
		{name: "flask", framework: "flask", runtime: "wsgi-gunicorn", sourceName: "app.py", source: "from flask import Flask\napp = Flask(__name__)\n", lockfile: "poetry.lock"},
		{name: "express", framework: "express", runtime: "node-http", packageJSON: `{"scripts":{"start":"node app.js"},"dependencies":{"express":"1"}}`, lockfile: "package-lock.json", sourceName: "app.js", source: "const express = require('express'); const app = express(); app.listen(process.env.PORT, process.env.HOST);"},
		{name: "fastify", framework: "fastify", runtime: "node-http", packageJSON: `{"scripts":{"start":"node app.js"},"dependencies":{"fastify":"1"}}`, lockfile: "pnpm-lock.yaml", sourceName: "app.js", source: "const fastify = require('fastify')(); fastify.listen({port: process.env.PORT, host: process.env.HOST});"},
		{name: "nestjs", framework: "nestjs", runtime: "nestjs", packageJSON: `{"scripts":{"start:prod":"node dist/main.js","build":"nest build"},"dependencies":{"@nestjs/core":"1"}}`, lockfile: "yarn.lock", sourceName: "main.ts", source: "NestFactory.create(AppModule); Transport.GRPC; process.env.HOST; process.env.HTTP_PORT; process.env.RPC_PORT;"},
		{name: "bun-serve", framework: "bun-serve", runtime: "bun-http", packageJSON: `{"scripts":{"start":"bun app.ts"}}`, lockfile: "bun.lock", sourceName: "app.ts", source: "Bun.serve({hostname: Bun.env.HOST, port: Bun.env.PORT});"},
		{name: "elysia", framework: "elysia", runtime: "bun-http", packageJSON: `{"scripts":{"start":"bun app.ts"},"dependencies":{"elysia":"1"}}`, lockfile: "bun.lock", sourceName: "app.ts", source: "new Elysia().listen({hostname: Bun.env.HOST, port: Bun.env.PORT});"},
		{name: "hono", framework: "hono", runtime: "bun-http", packageJSON: `{"scripts":{"start":"bun app.ts"},"dependencies":{"hono":"1"}}`, lockfile: "bun.lock", sourceName: "app.ts", source: "const app = new Hono(); Bun.serve({fetch: app.fetch, hostname: Bun.env.HOST, port: Bun.env.PORT});"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newAnalyzerRepository(t, test.name+"-service")
			if test.packageJSON != "" {
				writeAnalyzerFile(t, filepath.Join(repository, "package.json"), test.packageJSON)
			} else {
				writeAnalyzerFile(t, filepath.Join(repository, "pyproject.toml"), "[project]\nname = \"service\"\n")
			}
			writeAnalyzerFile(t, filepath.Join(repository, test.lockfile), "")
			writeAnalyzerFile(t, filepath.Join(repository, test.sourceName), test.source)
			analysis, matched, err := AnalyzeRepository(repository)
			if err != nil || !matched {
				t.Fatalf("analysis matched=%t error=%v", matched, err)
			}
			if analysis.Framework != test.framework || analysis.Runtime != test.runtime {
				t.Fatalf("framework/runtime = %s/%s, want %s/%s", analysis.Framework, analysis.Runtime, test.framework, test.runtime)
			}
			if len(analysis.EffectiveKinds()) == 0 || analysis.Runner.Prepare == nil {
				t.Fatalf("incomplete analysis: %#v", analysis)
			}
		})
	}
}

func TestTypedAnalyzerDjango(t *testing.T) {
	repository := newAnalyzerRepository(t, "django-service")
	writeAnalyzerFile(t, filepath.Join(repository, "uv.lock"), "")
	writeAnalyzerFile(t, filepath.Join(repository, "manage.py"), "import os\nos.environ.setdefault('DJANGO_SETTINGS_MODULE', 'site.settings')\n")
	writeAnalyzerFile(t, filepath.Join(repository, "site", "wsgi.py"), "application = object()\n")
	analysis, matched, err := AnalyzeRepository(repository)
	if err != nil || !matched || analysis.Framework != "django" || !reflect.DeepEqual(analysis.Runner.Run, []string{"uv", "run", "gunicorn", "site.wsgi:application"}) {
		t.Fatalf("Django analysis = %#v, matched=%t, error=%v", analysis, matched, err)
	}
}

func TestTypedAnalyzerGoFrameworkMatrix(t *testing.T) {
	tests := []struct {
		name      string
		framework string
		runtime   string
		moduleDep string
		source    string
		kind      string
	}{
		{name: "stdlib", framework: "go", runtime: "go-generic", moduleDep: "", source: `import http "net/http"\nfunc main(){ http.ListenAndServe(\":1\", nil) }`, kind: RepositoryKindHTTP},
		{name: "gin", framework: "gin", runtime: "go-generic", moduleDep: "github.com/gin-gonic/gin v1.0.0", source: `import gin "github.com/gin-gonic/gin"\nfunc main(){ gin.Default().Run() }`, kind: RepositoryKindHTTP},
		{name: "echo", framework: "echo", runtime: "go-generic", moduleDep: "github.com/labstack/echo/v4 v4.0.0", source: `import echo "github.com/labstack/echo/v4"\nfunc main(){ echo.New().Start(\":1\") }`, kind: RepositoryKindHTTP},
		{name: "fiber", framework: "fiber", runtime: "go-generic", moduleDep: "github.com/gofiber/fiber/v2 v2.0.0", source: `import fiber "github.com/gofiber/fiber/v2"\nfunc main(){ fiber.New().Listen(\":1\") }`, kind: RepositoryKindHTTP},
		{name: "chi", framework: "chi", runtime: "go-generic", moduleDep: "github.com/go-chi/chi/v5 v5.0.0", source: `import (\n  chi "github.com/go-chi/chi/v5"\n  http "net/http"\n)\nfunc main(){ chi.NewRouter(); http.ListenAndServe(\":1\", nil) }`, kind: RepositoryKindHTTP},
		{name: "grpc", framework: "grpc-go", runtime: "go-generic", moduleDep: "google.golang.org/grpc v1.0.0", source: `import grpc "google.golang.org/grpc"\nfunc main(){ grpc.NewServer() }`, kind: RepositoryKindRPC},
		{name: "kratos", framework: "kratos", runtime: "kratos", moduleDep: "github.com/go-kratos/kratos/v2 v2.0.0", source: `import transport "github.com/go-kratos/kratos/v2/transport/http"\nfunc main(){ transport.NewServer() }`, kind: RepositoryKindHTTP},
		{name: "hertz", framework: "hertz", runtime: "hertz", moduleDep: "github.com/cloudwego/hertz v1.0.0", source: `import server "github.com/cloudwego/hertz/pkg/app/server"\nfunc main(){ server.Default() }`, kind: RepositoryKindHTTP},
		{name: "kitex", framework: "kitex", runtime: "kitex", moduleDep: "github.com/cloudwego/kitex v1.0.0", source: `import server "github.com/cloudwego/kitex/server"\nfunc main(){ server.NewServer() }`, kind: RepositoryKindRPC},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			name := test.name + "-service"
			repository := newAnalyzerRepository(t, name)
			module := "module example.com/" + name + "\n"
			if test.moduleDep != "" { module += "require " + test.moduleDep + "\n" }
			writeAnalyzerFile(t, filepath.Join(repository, "go.mod"), module)
			source := strings.ReplaceAll(test.source, `\n`, "\n")
			source = strings.ReplaceAll(source, `\"`, `"`)
			writeAnalyzerFile(t, filepath.Join(repository, "main.go"), "package main\n"+source+"\n")
			analysis, matched, err := AnalyzeRepository(repository)
			if err != nil || !matched || analysis.Framework != test.framework || analysis.Runtime != test.runtime || analysis.Kind != test.kind {
				t.Fatalf("analysis = %#v, matched=%t, error=%v", analysis, matched, err)
			}
		})
	}
}

func TestGoAnalyzerAcceptsSemanticImportVersionSuffix(t *testing.T) {
	repository := newAnalyzerRepository(t, "versioned-service")
	writeAnalyzerFile(t, filepath.Join(repository, "go.mod"), "module example.com/versioned-service/v2\n")
	writeAnalyzerFile(t, filepath.Join(repository, "go.sum"), "example.invalid/module v1.0.0 h1:test\n")
	writeAnalyzerFile(t, filepath.Join(repository, "main.go"), `package main
import (
  "net/http"
  "os"
)
func main(){ http.ListenAndServe(os.Getenv("HOST")+":"+os.Getenv("PORT"), nil) }
`)
	analysis, matched, err := AnalyzeRepository(repository)
	if err != nil || !matched || analysis.Framework != "go" {
		t.Fatalf("versioned Go analysis = %#v, matched=%t, error=%v", analysis, matched, err)
	}
}

func TestTypedAnalyzerJavaFrameworkMatrix(t *testing.T) {
	tests := []struct {
		name      string
		framework string
		runtime   string
		pom       string
		source    string
	}{
		{name: "spring-maven", framework: "spring-boot", runtime: "spring-boot", pom: `<project><modelVersion>4.0.0</modelVersion><artifactId>spring-maven-service</artifactId><version>1.0.0</version><dependencies><dependency><artifactId>spring-boot-starter-web</artifactId></dependency></dependencies><build><plugins><plugin><artifactId>spring-boot-maven-plugin</artifactId></plugin></plugins></build></project>`, source: "@SpringBootApplication\npublic class App { public static void main(String[] args){ SpringApplication.run(App.class,args); } }\n@RestController\nclass API {}"},
		{name: "quarkus", framework: "quarkus", runtime: "quarkus", pom: `<project><modelVersion>4.0.0</modelVersion><artifactId>quarkus-service</artifactId><version>1.0.0</version><dependencies><dependency><artifactId>quarkus-resteasy</artifactId></dependency></dependencies></project>`, source: `@Path("/") public class API {}`},
		{name: "micronaut", framework: "micronaut", runtime: "micronaut", pom: `<project><modelVersion>4.0.0</modelVersion><artifactId>micronaut-service</artifactId><version>1.0.0</version><dependencies><dependency><artifactId>micronaut-http-server-netty</artifactId></dependency></dependencies></project>`, source: `@Controller("/") public class API {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newAnalyzerRepository(t, test.name)
			writeAnalyzerFile(t, filepath.Join(repository, "mvnw"), "#!/bin/sh\n")
			writeAnalyzerFile(t, filepath.Join(repository, "pom.xml"), test.pom)
			writeAnalyzerFile(t, filepath.Join(repository, "src", "main", "java", "App.java"), test.source)
			analysis, matched, err := AnalyzeRepository(repository)
			if err != nil || !matched || analysis.Framework != test.framework || analysis.Runtime != test.runtime || analysis.Kind != RepositoryKindHTTP {
				t.Fatalf("analysis = %#v, matched=%t, error=%v", analysis, matched, err)
			}
		})
	}
}

func TestKnownDynamicFrameworkFailsClosedWithoutRuntimePort(t *testing.T) {
	repository := newAnalyzerRepository(t, "unsafe-express-service")
	writeAnalyzerFile(t, filepath.Join(repository, "package.json"), `{"scripts":{"start":"node app.js"},"dependencies":{"express":"1"}}`)
	writeAnalyzerFile(t, filepath.Join(repository, "package-lock.json"), "{}")
	writeAnalyzerFile(t, filepath.Join(repository, "app.js"), "const app = require('express')(); app.listen(8080, process.env.HOST);")
	_, matched, err := AnalyzeRepository(repository)
	if err == nil || matched || !strings.Contains(err.Error(), "does not consume runtime PORT") {
		t.Fatalf("unsafe Express analysis matched=%t error=%v", matched, err)
	}
}

func TestPythonHashedRequirementsAcceptsPipCompileContinuation(t *testing.T) {
	data := []byte("fastapi==0.116.1 \\\n    --hash=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \\\n    --hash=sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n")
	if err := validateHashedRequirements(data); err != nil {
		t.Fatal(err)
	}
	if err := validateHashedRequirements([]byte("fastapi==0.116.1\n")); err == nil {
		t.Fatal("unhashed pinned requirement was accepted")
	}
}

func TestDynamicAnalyzersIgnoreCommentedServerEvidence(t *testing.T) {
	node := newAnalyzerRepository(t, "commented-express")
	writeAnalyzerFile(t, filepath.Join(node, "package.json"), `{"scripts":{"start":"node app.js"},"dependencies":{"express":"1"}}`)
	writeAnalyzerFile(t, filepath.Join(node, "package-lock.json"), "{}")
	writeAnalyzerFile(t, filepath.Join(node, "app.js"), "// const app = express(); app.listen(process.env.PORT, process.env.HOST)\n")
	if _, matched, err := AnalyzeRepository(node); err == nil || matched || !strings.Contains(err.Error(), "no supported server bootstrap") {
		t.Fatalf("commented Express evidence matched=%t error=%v", matched, err)
	}
	python := newAnalyzerRepository(t, "commented-fastapi")
	writeAnalyzerFile(t, filepath.Join(python, "uv.lock"), "version = 1\n")
	writeAnalyzerFile(t, filepath.Join(python, "main.py"), "# app = FastAPI()\n")
	if _, matched, err := AnalyzeRepository(python); err != nil || matched {
		t.Fatalf("commented FastAPI evidence matched=%t error=%v", matched, err)
	}
}
