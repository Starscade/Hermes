.POSIX:


GIT_BRANCH = $(shell git branch --show-current)
GIT_TAG = $(shell git describe --tags)
LDFLAGS = -s -w -X 'main.version=$(GIT_TAG)-$(GIT_BRANCH)'


all:

	@go mod tidy          && \
	go fmt                && \
	CGO_ENABLED=0            \
	go build                 \
		-ldflags="$(LDFLAGS)"  \
		-o ~/.local/bin/hermes \
		-v                     \
		-x                     \
		.


dock:

	@docker build --no-cache -t hermes .


