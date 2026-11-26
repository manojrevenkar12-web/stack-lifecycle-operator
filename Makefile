ENVTEST_K8S ?= 1.33.0
ENVTEST_DIR ?= $(shell pwd)/bin/envtest

.PHONY: test test-go test-python lint render lab-dev report check envtest image

test: test-go test-python

envtest:
	go run sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.21 use $(ENVTEST_K8S) --bin-dir $(ENVTEST_DIR) -p path > .envtest-path

test-go: envtest
	go vet ./...
	KUBEBUILDER_ASSETS=$$(cat .envtest-path) go test ./...

test-python:
	cd tools/stackctl && python -m pytest -q && ruff check . && ruff format --check .

render:
	go run ./cmd/render -f config/samples/platformstack.yaml > rendered.yaml

lint: render
	cat rendered.yaml config/rbac/*.yaml config/manager/manager.yaml | (cd tools/stackctl && python -m stackctl.cli openshift-lint -)

lab-dev:
	go run ./cmd/lab sim --split dev
	go run ./cmd/lab report

report:
	go run ./cmd/lab report

check:
	go run ./cmd/lab report --check

image:
	docker build -t stack-lifecycle-operator:dev .
