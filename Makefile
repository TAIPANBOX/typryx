.PHONY: test gates all
all: test gates
test:
	go test ./... -race
gates:
	./scripts/features-are-bound.sh
	./scripts/readme-numbers.sh
	./scripts/one-way-out.sh
	./scripts/no-secrets.sh
	./scripts/gates-have-teeth.sh
