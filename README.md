# Hermes

Hermes is a command-line interface tool for sending emails via SMTP.

## Installation

- Build and install to `~/.local/bin`: `make`.
- Build docker image: `make dock`.
- Build and run with docker compose: `make run`.

## Usage

Pipe the email body into the command: `echo "Hello World" | HERMES_USER=user@gmail.com HERMES_PASS=password hermes -to=dest@example.com -subject="Test Email"`.
