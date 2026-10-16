# Hermes

A Model Context Protocol (MCP) server for SMTP and IMAP.

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

The server communicates via **Stdio**, meaning it is designed to be started by an MCP client. You must provide the following environment variables to the client process for authentication and connectivity:

| Variable | Description |
|----------|-------------|
| `IMAP_HOST` | IMAP server hostname |
| `IMAP_PORT` | IMAP server port |
| `SMTP_HOST` | SMTP server hostname |
| `SMTP_PORT` | SMTP server port |
| `EMAIL_USER` | Email username/address |
| `EMAIL_PASS` | Email password or App Password |

## MCP Tools

### `list_emails`
Lists summaries of emails from the INBOX.
- **Arguments**: `unreadOnly` (boolean, optional)
- **Returns**: A formatted list of emails including sequence number, sender, date, and subject.

### `read_email`
Reads the cleaned content of a specific email.
- **Arguments**: `seq` (string, required) - The sequence number from `list_emails`.
- **Returns**: The body content of the email (prioritizing plain text over HTML).

### `send_email`
Sends a new email via SMTP.
- **Arguments**: 
    - `to` (string, required): Recipient email address.
    - `subject` (string, required): Email subject.
    - `body` (string, required): Email body content.
    - `attachments` (array, optional): A list of objects containing `path` (absolute path to file) and `mimeType` (e.g., `application/pdf`).
- **Returns**: Confirmation of successful delivery.

## Usage

```json
{
  "mcpServers": {
    "hermes": {
      "command": "/path/to/your/bin/hermes",
      "env": {
        "IMAP_HOST": "imap.gmail.com",
        "IMAP_PORT": "993",
        "SMTP_HOST": "smtp.gmail.com",
        "SMTP_PORT": "465",
        "EMAIL_USER": "your-email@gmail.com",
        "EMAIL_PASS": "your-app-password"
      }
    }
  }
}
```
