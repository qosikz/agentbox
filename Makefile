APP := agentbox
PKG := ./...

.PHONY: fmt test build lint clean

fmt:
	go fmt $(PKG)

test:
	go test $(PKG)

build:
	go build -o bin/$(APP) ./cmd/agentbox

lint:
	go vet $(PKG)

clean:
	rm -rf bin
