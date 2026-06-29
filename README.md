# Hermes

A unified, RESTful API for SMTP and IMAP.

## Installation

- Build and install to `~/.local/bin`: `make`.
- Build docker image: `make dock`.
- Build and run with docker compose: `make run`.

## Usage

###### READ MAIL
`curl -u 'foo@bar.com:password' localhost:8413/unread`

###### WRITE MAIL
```
curl -u 'foo@bar.com:password' localhost:8413/outbox \
     -d '{
	"to": [
		"baz@bar.com"
	],
	"subject": "Hello, there!",
	"body": "Lorem ipsum, et cetera."
}'
```
