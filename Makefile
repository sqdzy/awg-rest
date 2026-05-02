# awg-rest top-level Makefile.
# Use GNU Make (or Make on Windows: `make` from Git Bash / WSL).

GO         ?= go
GOFLAGS    ?=
PKG        := ./...
LDFLAGS    := -s -w
TEST_FLAGS ?= -timeout=180s

.PHONY: all
all: build

.PHONY: build
build:
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" ./cmd/awg-api
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" ./cmd/awg-worker
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" ./cmd/awg-node-agent

.PHONY: vet
vet:
	$(GO) vet $(PKG)

.PHONY: test
test: vet
	$(GO) test $(TEST_FLAGS) $(PKG)

.PHONY: integration
integration:
	TESTCONTAINERS_RYUK_DISABLED=true $(GO) test $(TEST_FLAGS) -tags=integration ./test/integration/...

.PHONY: e2e
e2e:
	TESTCONTAINERS_RYUK_DISABLED=true $(GO) test $(TEST_FLAGS) -tags=e2e ./test/e2e/...

.PHONY: e2e-real-awg
e2e-real-awg:
	# Requires Linux + amneziawg-tools + amneziawg kernel module.
	$(GO) test $(TEST_FLAGS) -tags="e2e linux_awg" ./test/e2e/...

.PHONY: lint
lint:
	$(GO) vet $(PKG)

.PHONY: tidy
tidy:
	$(GO) mod tidy

.PHONY: docker
docker:
	docker build -f deploy/docker/Dockerfile.all-in-one --target awg-rest-all-in-one -t awg-rest/all-in-one:dev .

.PHONY: docker-all-in-one
docker-all-in-one: docker

.PHONY: compose-up
compose-up:
	docker compose up -d

.PHONY: compose-down
compose-down:
	docker compose down -v

.PHONY: clean
clean:
	rm -f awg-api awg-worker awg-node-agent
