package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	DefaultEmailLimit = 20
	NetworkTimeout    = 30 * time.Second
	MimeVersion       = "1.0"
	ContentTypeHTML   = "text/html; charset=\"UTF-8\""
)

var (
	imapClient *imapclient.Client
	imapMutex  sync.Mutex
	Version    = "dev"
)

type Config struct {
	ImapHost  string
	ImapPort  string
	SmtpHost  string
	SmtpPort  string
	EmailUser string
	EmailPass string
}

func LoadConfig() *Config {
	return &Config{
		ImapHost:  os.Getenv("IMAP_HOST"),
		ImapPort:  os.Getenv("IMAP_PORT"),
		SmtpHost:  os.Getenv("SMTP_HOST"),
		SmtpPort:  os.Getenv("SMTP_PORT"),
		EmailUser: os.Getenv("EMAIL_USER"),
		EmailPass: os.Getenv("EMAIL_PASS"),
	}
}

type EmailServer struct {
	cfg *Config
}

// Tool Parameters
type ListEmailsParams struct {
	UnreadOnly bool `json:"unreadOnly"`
}

type ReadEmailParams struct {
	Seq string `json:"seq"`
}

type Attachment struct {
	Path     string `json:"path"`
	MimeType string `json:"mimeType"`
}

type SendEmailParams struct {
	To          string       `json:"to"`
	Subject     string       `json:"subject"`
	Body        string       `json:"body"`
	Attachments []Attachment `json:"attachments"`
}

func (s *EmailServer) getIMAPClient(ctx context.Context) (*imapclient.Client, error) {
	imapMutex.Lock()
	defer imapMutex.Unlock()

	if imapClient != nil {
		if err := imapClient.Noop().Wait(); err == nil {
			return imapClient, nil
		}
		log.Println("INFO: IMAP connection lost, reconnecting...")
		imapClient.Logout().Wait()
		imapClient = nil
	}

	if s.cfg.ImapHost == "" || s.cfg.ImapPort == "" {
		return nil, fmt.Errorf("IMAP_HOST or IMAP_PORT not set")
	}

	type dialResult struct {
		client *imapclient.Client
		err    error
	}
	resChan := make(chan dialResult, 1)

	go func() {
		addr := fmt.Sprintf("%s:%s", s.cfg.ImapHost, s.cfg.ImapPort)
		c, err := imapclient.DialTLS(addr, nil)
		if err != nil {
			resChan <- dialResult{nil, err}
			return
		}

		if err := c.Login(s.cfg.EmailUser, s.cfg.EmailPass).Wait(); err != nil {
			c.Logout().Wait()
			resChan <- dialResult{nil, err}
			return
		}
		resChan <- dialResult{c, nil}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(NetworkTimeout):
		return nil, fmt.Errorf("IMAP connection timed out after %v", NetworkTimeout)
	case res := <-resChan:
		if res.err != nil {
			return nil, res.err
		}
		imapClient = res.client
		return imapClient, nil
	}
}

func (s *EmailServer) listEmails(ctx context.Context, req *mcp.CallToolRequest, params ListEmailsParams) (*mcp.CallToolResult, any, error) {
	c, err := s.getIMAPClient(ctx)
	if err != nil {
		return nil, nil, err
	}

	mbox, selectErr := c.Select("INBOX", nil).Wait()
	if selectErr != nil {
		return nil, nil, selectErr
	}

	var seqSet imap.SeqSet
	if params.UnreadOnly {
		criteria := &imap.SearchCriteria{NotFlag: []imap.Flag{"\\Seen"}}
		ids, searchErr := c.Search(criteria, nil).Wait()
		if searchErr != nil {
			return nil, nil, searchErr
		}
		uids := ids.AllUIDs()
		if len(uids) == 0 {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "No unread emails found."}},
			}, nil, nil
		}
		seqSet = imap.SeqSet{}
		for _, uid := range uids {
			seqSet.AddNum(uint32(uid))
		}
	} else {
		start := uint32(1)
		if mbox.NumMessages > DefaultEmailLimit {
			start = mbox.NumMessages - uint32(DefaultEmailLimit-1)
		}
		seqSet = imap.SeqSet{}
		seqSet.AddRange(start, mbox.NumMessages)
	}

	options := &imap.FetchOptions{Envelope: true}
	fetchCmd := c.Fetch(seqSet, options)
	defer fetchCmd.Close()

	var summary strings.Builder
	summary.WriteString(fmt.Sprintf("Inbox Summary (Total: %d):\n", mbox.NumMessages))
	count := 0
	for {
		msg := fetchCmd.Next()
		if msg == nil {
			break
		}
		count++
		item := msg.Next()
		if envItem, ok := item.(imapclient.FetchItemDataEnvelope); ok {
			env := envItem.Envelope
			from := "Unknown"
			if len(env.From) > 0 {
				from = env.From[0].Addr()
			}
			summary.WriteString(fmt.Sprintf("[%d] From: %s | Date: %s | Subject: %s\n", msg.SeqNum, from, env.Date.Format("2006-01-02 15:04"), env.Subject))
		}
	}

	if count == 0 {
		summary.WriteString("No emails found matching criteria.")
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: summary.String()}},
	}, nil, nil
}

func (s *EmailServer) readEmail(ctx context.Context, req *mcp.CallToolRequest, params ReadEmailParams) (*mcp.CallToolResult, any, error) {
	seqNum, err := strconv.ParseUint(params.Seq, 10, 32)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid sequence number: %w", err)
	}

	c, err := s.getIMAPClient(ctx)
	if err != nil {
		return nil, nil, err
	}

	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		return nil, nil, err
	}

	seqSet := imap.SeqSetNum(uint32(seqNum))
	options := &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{{}},
	}
	fetchCmd := c.Fetch(seqSet, options)
	defer fetchCmd.Close()

	msg := fetchCmd.Next()
	if msg == nil {
		return nil, nil, fmt.Errorf("email not found")
	}

	var rawBody []byte
	for {
		item := msg.Next()
		if item == nil {
			break
		}
		if data, ok := item.(imapclient.FetchItemDataBodySection); ok {
			b, _ := io.ReadAll(data.Literal)
			rawBody = append(rawBody, b...)
		}
	}

	cleanText, err := parseEmailBody(rawBody)
	if err != nil {
		log.Printf("Error parsing email body: %v", err)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(rawBody)}},
		}, nil, nil
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: cleanText}},
	}, nil, nil
}

func parseEmailBody(raw []byte) (string, error) {
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		return "", err
	}

	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		body, _ := io.ReadAll(msg.Body)
		return string(body), nil
	}

	if mediaType == "text/plain" {
		body, _ := io.ReadAll(msg.Body)
		return string(body), nil
	}

	if mediaType == "multipart/" {
		mr := multipart.NewReader(msg.Body, params["boundary"])
		var plainText strings.Builder
		var htmlText strings.Builder

		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", err
			}

			pType, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
			content, _ := io.ReadAll(p)

			if pType == "text/plain" {
				plainText.Write(content)
			} else if pType == "text/html" {
				htmlText.Write(content)
			}
		}

		if plainText.Len() > 0 {
			return plainText.String(), nil
		}
		return htmlText.String(), nil
	}

	body, _ := io.ReadAll(msg.Body)
	return string(body), nil
}

func (s *EmailServer) sendEmail(ctx context.Context, req *mcp.CallToolRequest, params SendEmailParams) (*mcp.CallToolResult, any, error) {
	if _, err := mail.ParseAddress(params.To); err != nil {
		return nil, nil, fmt.Errorf("invalid recipient email address: %w", err)
	}

	auth := smtp.PlainAuth("", s.cfg.EmailUser, s.cfg.EmailPass, s.cfg.SmtpHost)
	addr := fmt.Sprintf("%s:%s", s.cfg.SmtpHost, s.cfg.SmtpPort)

	buf := bytes.NewBuffer(nil)

	buf.WriteString(fmt.Sprintf("From: %s\r\n", s.cfg.EmailUser))
	buf.WriteString(fmt.Sprintf("To: %s\r\n", params.To))
	buf.WriteString(fmt.Sprintf("Subject: %s\r\n", params.Subject))
	buf.WriteString(fmt.Sprintf("MIME-Version: %s\r\n", MimeVersion))

	if len(params.Attachments) == 0 {
		buf.WriteString(fmt.Sprintf("Content-Type: %s\r\n\r\n", ContentTypeHTML))
		buf.WriteString(params.Body)
	} else {
		writer := multipart.NewWriter(buf)
		buf.WriteString(fmt.Sprintf("Content-Type: multipart/mixed; boundary=%s\r\n\r\n", writer.Boundary()))

		// Part 1: The Body
		h := make(textproto.MIMEHeader)
		h.Set("Content-Type", ContentTypeHTML)
		bodyPart, err := writer.CreatePart(h)
		if err != nil {
			return nil, nil, err
		}
		bodyPart.Write([]byte(params.Body))

		// Parts 2..N: The Attachments
		for _, att := range params.Attachments {
			data, err := os.ReadFile(att.Path)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to read attachment file at %s: %w", att.Path, err)
			}

			attH := make(textproto.MIMEHeader)
			contentType := att.MimeType
			if contentType == "" {
				contentType = "application/octet-stream"
			}
			attH.Set("Content-Type", contentType)
			// Explicitly set Base64 encoding to prevent binary corruption/metadata loss
			attH.Set("Content-Transfer-Encoding", "base64")

			fileName := att.Path
			if idx := strings.LastIndex(att.Path, string(os.PathSeparator)); idx != -1 {
				fileName = att.Path[idx+1:]
			}
			attH.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileName))

			attPart, err := writer.CreatePart(attH)
			if err != nil {
				return nil, nil, err
			}

			// Encode the file content as Base64
			encoder := base64.NewEncoder(base64.StdEncoding, attPart)
			if _, err := encoder.Write(data); err != nil {
				return nil, nil, err
			}
			encoder.Close()
		}
		writer.Close()
	}

	errChan := make(chan error, 1)
	go func() {
		errChan <- smtp.SendMail(addr, auth, s.cfg.EmailUser, []string{params.To}, buf.Bytes())
	}()

	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case err := <-errChan:
		if err != nil {
			return nil, nil, err
		}
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Successfully sent email to %s with %d attachments", params.To, len(params.Attachments))}},
	}, nil, nil
}

func main() {
	s := &EmailServer{
		cfg: LoadConfig(),
	}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "hermes",
		Version: Version,
	}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_emails",
		Description: "Lists emails with summaries (Subject, Date, From).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"unreadOnly": map[string]any{
					"type":        "boolean",
					"description": "If true, only returns unread emails.",
				},
			},
		},
	}, s.listEmails)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "read_email",
		Description: "Reads the cleaned content of an email by sequence number.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"seq": map[string]any{
					"type":        "string",
					"description": "The sequence number of the email.",
				},
			},
			"required": []string{"seq"},
		},
	}, s.readEmail)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "send_email",
		Description: "Sends an email to a recipient, optionally with local file attachments.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"to": map[string]any{
					"type":        "string",
					"description": "Recipient email address",
				},
				"subject": map[string]any{
					"type":        "string",
					"description": "Email subject",
				},
				"body": map[string]any{
					"type":        "string",
					"description": "Email body content",
				},
				"attachments": map[string]any{
					"type":        "array",
					"description": "List of local files to attach",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"path": map[string]any{
								"type":        "string",
								"description": "The absolute path to the file on the local system",
							},
							"mimeType": map[string]any{
								"type":        "string",
								"description": "The MIME type of the file (e.g., 'image/jpeg', 'application/pdf')",
							},
						},
						"required": []string{"path", "mimeType"},
					},
				},
			},
			"required": []string{"to", "subject", "body"},
		},
	}, s.sendEmail)

	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
