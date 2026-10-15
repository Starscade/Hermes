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
	"sync"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	imapClient *imapclient.Client
	imapMutex  sync.Mutex
)

type EmailServer struct{}

// Tool Parameters
type ListEmailsParams struct {
	UnreadOnly bool `json:"unreadOnly" jsonschema:"If true, only returns unread emails."`
}

type ReadEmailParams struct {
	Seq string `json:"seq" jsonschema:"The sequence number of the email."`
}

type SendEmailParams struct {
	To      string `json:"to" jsonschema:"Recipient email address"`
	Subject string `json:"subject" jsonschema:"Email subject"`
	Body    string `json:"body" jsonschema:"Email body content"`
}

func getIMAPClient() (*imapclient.Client, error) {
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

	host := os.Getenv("IMAP_HOST")
	port := os.Getenv("IMAP_PORT")
	user := os.Getenv("EMAIL_USER")
	pass := os.Getenv("EMAIL_PASS")

	if host == "" || port == "" {
		return nil, fmt.Errorf("IMAP_HOST or IMAP_PORT not set")
	}

	c, err := imapclient.DialTLS(fmt.Sprintf("%s:%s", host, port), nil)
	if err != nil {
		return nil, fmt.Errorf("dial tls failed: %w", err)
	}

	if err := c.Login(user, pass).Wait(); err != nil {
		c.Logout().Wait()
		return nil, fmt.Errorf("login failed: %w", err)
	}

	imapClient = c
	return imapClient, nil
}

func (s *EmailServer) listEmails(ctx context.Context, req *mcp.CallToolRequest, params ListEmailsParams) (*mcp.CallToolResult, any, error) {
	c, err := getIMAPClient()
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
		if mbox.NumMessages > 20 {
			start = mbox.NumMessages - 19
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

	c, err := getIMAPClient()
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

	var bodyBuilder strings.Builder
	msg := fetchCmd.Next()
	if msg != nil {
		for {
			item := msg.Next()
			if item == nil {
				break
			}
			if data, ok := item.(imapclient.FetchItemDataBodySection); ok {
				b, _ := io.ReadAll(data.Literal)
				bodyBuilder.Write(b)
			}
		}
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: bodyBuilder.String()}},
	}, nil, nil
}

func (s *EmailServer) sendEmail(ctx context.Context, req *mcp.CallToolRequest, params SendEmailParams) (*mcp.CallToolResult, any, error) {
	smtpHost := os.Getenv("SMTP_HOST")
	smtpPort := os.Getenv("SMTP_PORT")
	user := os.Getenv("EMAIL_USER")
	pass := os.Getenv("EMAIL_PASS")

	auth := smtp.PlainAuth("", user, pass, smtpHost)
	addr := fmt.Sprintf("%s:%s", smtpHost, smtpPort)
	msg := []byte("From: " + user + "\r\nTo: " + params.To + "\r\nSubject: " + params.Subject + "\r\n\r\n" + params.Body + "\r\n")

	if err := smtp.SendMail(addr, auth, user, []string{params.To}, msg); err != nil {
		return nil, nil, err
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Successfully sent email to %s", params.To)}},
	}, nil, nil
}

func main() {
	s := &EmailServer{}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "email-mcp-server",
		Version: "1.0.0",
	}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_emails",
		Description: "Lists emails with summaries (Subject, Date, From).",
	}, s.listEmails)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "read_email",
		Description: "Reads raw email content by sequence number.",
	}, s.readEmail)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "send_email",
		Description: "Sends an email to a recipient.",
	}, s.sendEmail)

	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
