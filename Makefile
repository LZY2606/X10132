GO?=go
PACKAGE_DIRS=       $(shell $(GO) list -f '{{ .Dir }}' ./...|grep -v 'lesismal/nbio/examples')
PACKAGES=           $(shell $(GO) list ./...|grep -v 'lesismal/nbio/examples')
.PHONY: all vet lint verify

all: vet lint test

verify:
	./verify.sh

vet:
	$(GO) vet $(PACKAGES)

lint:
	golangci-lint run $(PACKAGE_DIRS)

test:
	$(GO) test -v $(PACKAGES)

clean:
	rm -rf ./autobahn/bin/*
	rm -rf ./autobahn/report/*

autobahn:
	chmod +x ./autobahn/script/run.sh & ./autobahn/script/run.sh

.PHONY: all vet lint verify test clean  autobahn
