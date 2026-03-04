.PHONY: test test-race coverage ci-check

test:
	go test ./...

test-race:
	go test -race ./...

coverage:
	./scripts/coverage.sh 80

ci-check: test test-race coverage
