# Hermes

A Model Context Protocol server for IMAP and SMTP.

## Installation

### Build and Install
Build and install the binary to `~/.local/bin`:
```sh
make
```

### Docker
Or build a docker image:
```sh
make dock
```

## Configuration

The server requires the following environment variables to be set for authentication and connectivity:

| Variable | Description | Example |
|----------|-------------|---------|
| `IMAP_HOST` | IMAP server hostname | `imap.gmail.com` |
| `IMAP_PORT` | IMAP server port | `993` |
| `SMTP_HOST` | SMTP server hostname | `smtp.gmail.com` |
| `SMTP_PORT` | SMTP server port | `465` |
| `EMAIL_USER` | Email username/address | `user@example.com` |
| `EMAIL_PASS` | Email password or App Password | `your-password` |
| `HERMES_PORT`| Server port (defaults to 8080) | `8080` |

## MCP Tools

### `list_emails`
Lists summaries of emails from the INBOX.
- **Arguments**: `unreadOnly` (boolean, optional)
- **Returns**: A formatted list of emails including sequence number, sender, date, and subject.

### `read_email`
Reads the raw content of a specific email.
- **Arguments**: `seq` (string, required) - The sequence number from `list_emails`.
- **Returns**: The body content of the email.

### `send_email`
Sends a new email via SMTP.
- **Arguments**: 
    - `to` (string, required)
    - `subject` (string, required)
    - `body` (string, required)
- **Returns**: Confirmation of successful delivery.

## Usage

Should work with any modern MCP client.

### Example Request
```sh
curl -X POST http://localhost:8080/ \
     -H "Content-Type: application/json" \
     -d '{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}}'
```
