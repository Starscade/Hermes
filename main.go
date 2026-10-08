package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/smtp"
	"os"
	"strconv"
	"strings"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- Types ---

type EmailHeader struct {
	UID     uint32   `json:"uid"`
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Date    string   `json:"date"`
}

type EmailFull struct {
	UID      uint32   `json:"uid"`
	From     string   `json:"from"`
	To       []string `json:"to"`
	Subject  string   `json:"subject"`
	Date     string   `json:"date"`
	Body     string   `json:"body"`
	MimeType string   `json:"mime_type"`
}

type FetchInputs struct {
	UnreadOnly bool `json:"unread_only,omitempty"`
}

type ReadInputs struct {
	UID uint32 `json:"uid"`
}

type SendInputs struct {
	To      []string `json:"to"`
	Cc      []string `json:"cc,omitempty"`
	Bcc     []string `json:"bcc,omitempty"`
	Subject string   `json:"subject"`
	Body    string   `json:"body"`
}

// --- IMAP Logic ---

func getIMAPClient() (*client.Client, error) {
	user := os.Getenv("EMAIL_USER")
	pass := os.Getenv("EMAIL_PASS")
	host := getEnv("IMAP_HOST", "imap.gmail.com")
	port := getEnv("IMAP_PORT", "993")

	c, err := client.DialTLS(fmt.Sprintf("%s:%s", host, port), nil)
	if err != nil {
		return nil, err
	}

	if err := c.Login(user, pass); err != nil {
		c.Logout()
		return nil, err
	}

	return c, nil
}

func handleFetchEmails(ctx context.Context, input FetchInputs) (interface{}, error) {
	c, err := getIMAPClient()
	if err != nil {
		return nil, err
	}
	defer c.Logout()

	mbox, err := c.Select("INBOX", false)
	if err != nil {
		return nil, err
	}

	var seqSet *imap.SeqSet
	if input.UnreadOnly {
		criteria := imap.NewSearchCriteria()
		criteria.WithoutFlags = []string{imap.SeenFlag}
		ids, err := c.Search(criteria)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return []EmailHeader{}, nil
		}
		seqSet = newSeqSet(ids)
	} else {
		seqSet = newSeqSetRange(1, mbox.Messages)
	}

	items := []imap.FetchItem{imap.FetchEnvelope}
	messages := make(chan *imap.Message, 10)
	done := make(chan error, 1)

	go func() {
		done <- c.Fetch(seqSet, items, messages)
	}()

	var headers []EmailHeader
	for msg := range messages {
		headers = append(headers, EmailHeader{
			UID:     msg.SeqNum,
			From:    msg.Envelope.From[0].Address(),
			Subject: msg.Envelope.Subject,
			Date:    msg.Envelope.Date.String(),
		})
		if len(headers) >= 20 {
			break
		}
	}

	if err := <-done; err != nil {
		return nil, err
	}

	return headers, nil
}

func handleReadEmail(ctx context.Context, input ReadInputs) (interface{}, error) {
	c, err := getIMAPClient()
	if err != nil {
		return nil, err
	}
	defer c.Logout()

	section := &imap.BodySectionName{}
	items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchBody}
	seqSet := newSeqSet([]uint32{input.UID})
	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)

	go func() {
		done <- c.Fetch(seqSet, items, messages)
	}()

	msg := <-messages
	if err := <-done; err != nil {
		return nil, err
	}

	if msg == nil {
		return nil, fmt.Errorf("email not found")
	}

	body := ""
	r := msg.GetBody(section)
	if r != nil {
		buf, err := io.ReadAll(r)
		if err == nil {
			body = string(buf)
		}
	}

	return EmailFull{
		UID:      msg.SeqNum,
		From:     msg.Envelope.From[0].Address(),
		Subject:  msg.Envelope.Subject,
		Date:     msg.Envelope.Date.String(),
		Body:     body,
		MimeType: "text/plain",
	}, nil
}

func handleSendEmail(ctx context.Context, input SendInputs) (interface{}, error) {
	user := os.Getenv("EMAIL_USER")
	pass := os.Getenv("EMAIL_PASS")
	host := getEnv("SMTP_HOST", "smtp.gmail.com")
	port := getEnv("SMTP_PORT", "587")

	msg := []byte("From: " + user + "\r\n" +
		"To: " + strings.Join(input.To, ",") + "\r\n" +
		"Cc: " + strings.Join(input.Cc, ",") + "\r\n" +
		"Subject: " + input.Subject + "\r\n\r\n" +
		input.Body)

	auth := smtp.PlainAuth("", user, pass, host)
	recipients := append([]string{}, input.To...)
	recipients = append(recipients, input.Cc...)
	recipients = append(recipients, input.Bcc...)

	err := smtp.SendMail(host+":"+port, auth, user, recipients, msg)
	if err != nil {
		return nil, err
	}

	return "Email sent successfully.", nil
}

// --- SeqSet Helpers ---

func newSeqSet(ids []uint32) *imap.SeqSet {
	ss := imap.SeqSet{}
	for _, id := range ids {
		ss.Add(strconv.FormatUint(uint64(id), 10))
	}
	return &ss
}

func newSeqSetRange(start, end uint32) *imap.SeqSet {
	ss := imap.SeqSet{}
	ss.AddRange(start, end)
	return &ss
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

// --- MCP Tool Wrappers ---

func FetchEmailsTool(ctx context.Context, req *mcp.CallToolRequest, input FetchInputs) (*mcp.CallToolResult, interface{}, error) {
	res, err := handleFetchEmails(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	return nil, res, nil
}

func ReadEmailTool(ctx context.Context, req *mcp.CallToolRequest, input ReadInputs) (*mcp.CallToolResult, interface{}, error) {
	res, err := handleReadEmail(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	return nil, res, nil
}

func SendEmailTool(ctx context.Context, req *mcp.CallToolRequest, input SendInputs) (*mcp.CallToolResult, interface{}, error) {
	res, err := handleSendEmail(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	return nil, res, nil
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "Email Tool",
		Version: "1.0.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "fetch_emails",
		Description: "Fetch a list of email headers",
	}, FetchEmailsTool)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_email",
		Description: "Read the full content of an email by UID",
	}, ReadEmailTool)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "send_email",
		Description: "Send an email",
	}, SendEmailTool)

	log.Println("Starting MCP Email Server...")
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
