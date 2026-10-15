.POSIX:


GIT_TAG = $(shell git describe --tags --always)
LDFLAGS = -s -w -X 'main.Version=$(GIT_TAG)'


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


