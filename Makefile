BINARY ?= dmux-verify

.PHONY: build test vet sync-resources clean

build: sync-resources
	go build -o $(BINARY) ./cmd/dmux-verify

install:
	go install ./cmd/dmux-verify

test:
	go test ./...

vet:
	go vet ./...

# Keep the embedded guide/policies in sync with the canonical resources/.
sync-resources:
	mkdir -p internal/guide/resources
	cp resources/durablemux-codecrafters-guide.md resources/stage_policies.json internal/guide/resources/

clean:
	rm -f $(BINARY)
