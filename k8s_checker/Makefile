.PHONY: k8s_checker
k8s_checker:
	go install -v ./go/k8s_checker/...

.PHONY: release
release:
	CGO_ENABLED=0 GOOS=linux go install -a ./go/...
	./build_docker_release

.PHONY: push
push:
	pushk --cluster=skia-public k8s_checker

.PHONY: push_corp
push_corp:
	pushk --cluster=skia-corp k8s_checker

include ../make/clusters.mk
