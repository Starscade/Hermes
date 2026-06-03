.POSIX:

all:

	@\
		go mod tidy \
		&& go fmt . \
		&& CGO_ENABLED=0 \
			go build \
				-ldflags="-s -w" \
				-v -x -o ~/.local/bin/hermes \
				.

dock:

	@docker build --no-cache -t hermes .


run:

	@docker compose --env-file .env up --remove-orphans
