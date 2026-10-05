BINDIR := $(HOME)/.local/bin
CMDS := mail-find send-mail

.PHONY: build install skills test test-live vet fmt fix check

build:
	go build ./...

# Built beside the target and renamed into place: `go build -o` leaves a
# window where the installed path is a partially written file, and agents
# call these binaries by that path.
install:
	@for c in $(CMDS); do \
		go build -o $(BINDIR)/$$c.new ./cmd/$$c && mv -f $(BINDIR)/$$c.new $(BINDIR)/$$c || exit 1; \
	done

# The in-repo skills, made global: symlinked into the stow-managed dotfiles
# skills dir, which ~/.claude/skills already points at. The link is tracked
# there and read on two machines, so it is relative to that directory: an
# absolute path would resolve under one home only.
skills:
	@for s in skills/*/; do \
		name=$$(basename $$s); \
		ln -sfn ../../../../$(patsubst $(HOME)/%,%,$(CURDIR))/skills/$$name $(HOME)/dotfiles/claude/.claude/skills/$$name && echo "linked $$name"; \
	done

test:
	go test -race ./...

# Talks to the real Gmail/Outlook accounts; add RECORD=1 to refresh cassettes.
test-live:
	go test -tags live ./internal/gmail ./internal/graph -run Live $(if $(RECORD),-record)

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# Go 1.27 modernizers must leave nothing to rewrite.
fix:
	@out=$$(go fix -diff ./...); if [ -n "$$out" ]; then echo "$$out"; exit 1; fi

check: vet fix test
