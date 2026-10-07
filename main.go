package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/smtp"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message/mail"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var version = ""

type Email struct {
	Body     string   `json:"body"`
	Cc       []string `json:"cc,omitempty"`
	Date     string   `json:"date"`
	From     string   `json:"from"`
	MimeType string   `json:"mime_type"`
	Subject  string   `json:"subject"`
	To       []string `json:"to,omitempty"`
}

type MailInputs struct{}

type SendInputs struct {
	To      []string `json:"to" jsonschema:"List of recipient email addresses"`
	Cc      []string `json:"cc,omitempty" jsonschema:"List of CC recipients"`
	Bcc     []string `json:"bcc,omitempty" jsonschema:"List of BCC recipients"`
	Subject string   `json:"subject" jsonschema:"The email subject"`
	Body    string   `json:"body" jsonschema:"The email body content"`
}

func minifyHTML(html string) string {
	re := regexp.MustCompile(`>\s+<`)
	return re.ReplaceAllString(html, "><")
}

func fetchUnread() ([]Email, error) {
	user := os.Getenv("EMAIL_USER")
	pass := os.Getenv("EMAIL_PASS")
	if user == "" || pass == "" {
		return nil, fmt.Errorf("EMAIL_USER or EMAIL_PASS environment variables not set")
	}

	host := getEnv("IMAP_HOST", "imap.gmail.com")
	port := getEnv("IMAP_PORT", "993")

	c, err := client.DialTLS(fmt.Sprintf("%s:%s", host, port), nil)
	if err != nil {
		return nil, err
	}
	defer c.Logout()

	if err := c.Login(user, pass); err != nil {
		return nil, err
	}
	mbox, err := c.Select("INBOX", false)
	if err != nil {
		return nil, err
	}

	if mbox.Messages == 0 {
		return []Email{}, nil
	}

	seqset := new(imap.SeqSet)
	seqset.AddRange(1, mbox.Messages)

	section := &imap.BodySectionName{}
	items := []imap.FetchItem{imap.FetchEnvelope, section.FetchItem()}

	messages := make(chan *imap.Message, mbox.Messages)
	done := make(chan error, 1)
	go func() { done <- c.Fetch(seqset, items, messages) }()

	var results []Email
	for msg := range messages {
		r := msg.GetBody(section)
		mr, err := mail.CreateReader(r)
		if err != nil {
			continue
		}

		var body string
		var mediaTypeFound string

		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				continue
			}

			switch h := p.Header.(type) {
			case *mail.InlineHeader:
				mediaType, _, err := mime.ParseMediaType(h.Get("Content-Type"))
				if err != nil {
					mediaType = "text/plain"
				}

				if mediaType == "text/plain" || mediaType == "text/html" {
					b, _ := io.ReadAll(p.Body)
					body, mediaTypeFound = string(b), mediaType
					if mediaType == "text/plain" {
						break
					}
				}
			}
		}

		body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
		if mediaTypeFound == "text/html" {
			body = minifyHTML(body)
		}

		fromAddr := ""
		if len(msg.Envelope.From) > 0 {
			fromAddr = msg.Envelope.From[0].Address()
		}

		results = append(results, Email{
			From:     fromAddr,
			Subject:  msg.Envelope.Subject,
			Date:     msg.Envelope.Date.Format(time.RFC3339),
			Body:     body,
			MimeType: mediaTypeFound,
		})
	}
	return results, <-done
}

func handleFetchEmails(ctx context.Context, req *mcp.CallToolRequest, input MailInputs) (*mcp.CallToolResult, any, error) {
	emails, err := fetchUnread()
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Error fetching emails: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	if len(emails) == 0 {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "No unread emails found."}},
		}, nil, nil
	}

	data, _ := json.MarshalIndent(emails, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, nil, nil
}

func handleSendEmail(ctx context.Context, req *mcp.CallToolRequest, input SendInputs) (*mcp.CallToolResult, any, error) {
	user := os.Getenv("EMAIL_USER")
	pass := os.Getenv("EMAIL_PASS")
	if user == "" || pass == "" {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "EMAIL_USER or EMAIL_PASS environment variables not set"}},
			IsError: true,
		}, nil, nil
	}

	host := getEnv("SMTP_HOST", "smtp.gmail.com")
	port := getEnv("SMTP_PORT", "587")

	msg := []byte("From: " + user + "\r\n" +
		"To: " + strings.Join(input.To, ",") + "\r\n" +
		"Cc: " + strings.Join(input.Cc, ",") + "\r\n" +
		"Subject: " + input.Subject + "\r\n\r\n" +
		input.Body)

	auth := smtp.PlainAuth("", user, pass, host)
	err := smtp.SendMail(host+":"+port, auth, user, append(input.To, append(input.Cc, input.Bcc...)...), msg)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Error sending email: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "Email sent successfully."}},
	}, nil, nil
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "hermes-mail",
		Version: version,
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "fetch_emails",
		Description: "Fetch unread emails from the INBOX using IMAP",
	}, handleFetchEmails)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "send_email",
		Description: "Send an email using SMTP",
	}, handleSendEmail)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
