VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMG ?= ghcr.io/bardiayaghmaie/bardiup:$(VERSION)
CONTROLLER_GEN ?= go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.23.0
GOLANGCI_LINT ?= go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
LDFLAGS := -s -w -X github.com/BardiaYaghmaie/bardiup/internal/version.Version=$(VERSION)

.PHONY: all
all: generate manifests build test

.PHONY: generate
generate: ## Generate DeepCopy implementations.
	$(CONTROLLER_GEN) object paths=./api/...

.PHONY: manifests
manifests: ## Generate CRDs and the controller ClusterRole into the Helm chart.
	$(CONTROLLER_GEN) crd paths=./api/... output:crd:artifacts:config=charts/bardiup/crds
	$(CONTROLLER_GEN) rbac:roleName=bardiup paths=./internal/controller/... output:rbac:artifacts:config=charts/bardiup/files

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: lint
lint:
	$(GOLANGCI_LINT) run ./...

.PHONY: test
test: ## Unit and integration tests (no cluster needed).
	go test -race -coverprofile=cover.out ./...

.PHONY: build
build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/bardiup ./cmd/bardiup

.PHONY: docker-build
docker-build:
	docker build --build-arg VERSION=$(VERSION) -t $(IMG) .

.PHONY: e2e
e2e: ## End-to-end test on a throwaway kind cluster (needs docker, kind, kubectl, helm).
	./hack/e2e.sh

.PHONY: stress
stress: ## Stress and chaos test on a throwaway multi-node kind cluster.
	./hack/stress.sh

.PHONY: fuzz
fuzz:
	go test ./internal/archive -run '^$$' -fuzz FuzzExtract -fuzztime 60s
	go test ./internal/retention -run '^$$' -fuzz FuzzEvaluate -fuzztime 60s

.PHONY: install-manifest
install-manifest: ## Render a single-file install manifest to dist/bardiup.yaml.
	mkdir -p dist
	printf 'apiVersion: v1\nkind: Namespace\nmetadata:\n  name: bardiup-system\n' > dist/bardiup.yaml
	for f in charts/bardiup/crds/*.yaml; do cat $$f >> dist/bardiup.yaml; done
	helm template bardiup charts/bardiup --namespace bardiup-system --set image.tag=$(VERSION:v%=%) >> dist/bardiup.yaml
