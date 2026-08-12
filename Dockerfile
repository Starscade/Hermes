FROM golang:alpine AS builder
WORKDIR /app
COPY go.mod go.sum main.go .
RUN go mod tidy \
	&& go fmt . \
	&& CGO_ENABLED=0 \
		go build \
			-ldflags="-s -w" \
			-v -x -o /hermes \
			.

FROM scratch
COPY --from=builder /hermes /usr/local/bin/hermes

CMD ["hermes"]
