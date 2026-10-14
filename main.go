package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// LogEntry defines the JSON structure for stderr logs to avoid corrupting stdio transport
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Message   string `json:"message"`
}

func jsonLog(level, message string) {
	entry := LogEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Level:     level,
		Message:   message,
	}
	bytes, _ := json.Marshal(entry)
	fmt.Fprintln(os.Stderr, string(bytes))
}

type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type JSONRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   interface{} `json:"error,omitempty"`
}

var (
	imapClient *imapclient.Client
	imapMutex  sync.Mutex
)

func getIMAPClient() (*imapclient.Client, error) {
	imapMutex.Lock()
	defer imapMutex.Unlock()

	if imapClient != nil {
		if err := imapClient.Noop().Wait(); err == nil {
			return imapClient, nil
		}
		jsonLog("INFO", "IMAP connection lost, reconnecting...")
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

func handleRequest(req JSONRPCRequest) JSONRPCResponse {
	if req.Method == "server/discover" {
		jsonLog("INFO", "Server discovery requested")
		return JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"serverInfo": map[string]interface{}{
					"name":    "email-mcp-server",
					"version": "1.0.0",
				},
				"capabilities": map[string]interface{}{
					"tools": map[string]interface{}{},
				},
			},
		}
	}

	if req.Method == "tools/list" {
		jsonLog("INFO", "Tools discovery requested")
		return JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"tools": []map[string]interface{}{
					{
						"name":        "list_emails",
						"description": "Lists emails with summaries (Subject, Date, From).",
						"inputSchema": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"unreadOnly": map[string]interface{}{
									"type":        "boolean",
									"description": "If true, only returns unread emails.",
								},
							},
						},
					},
					{
						"name":        "read_email",
						"description": "Reads raw email content by sequence number.",
						"inputSchema": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"seq": map[string]interface{}{
									"type":        "string",
									"description": "The sequence number of the email.",
								},
							},
							"required": []string{"seq"},
						},
					},
					{
						"name":        "send_email",
						"description": "Sends an email to a recipient.",
						"inputSchema": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"to":      map[string]interface{}{"type": "string", "description": "Recipient email address"},
								"subject": map[string]interface{}{"type": "string", "description": "Email subject"},
								"body":    map[string]interface{}{"type": "string", "description": "Email body content"},
							},
							"required": []string{"to", "subject", "body"},
						},
					},
				},
			},
		}
	}

	if req.Method == "tools/call" {
		var params struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32602, "message": "Invalid params"}}
		}

		switch params.Name {
		case "list_emails":
			unreadOnly, _ := params.Arguments["unreadOnly"].(bool)
			jsonLog("INFO", fmt.Sprintf("Tool 'list_emails' called (unreadOnly: %v)", unreadOnly))

			c, err := getIMAPClient()
			if err != nil {
				return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32000, "message": err.Error()}}
			}

			mbox, selectErr := c.Select("INBOX", nil).Wait()
			if selectErr != nil {
				return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32000, "message": selectErr.Error()}}
			}

			var seqSet imap.SeqSet
			if unreadOnly {
				criteria := &imap.SearchCriteria{NotFlag: []imap.Flag{"\\Seen"}}
				ids, searchErr := c.Search(criteria, nil).Wait()
				if searchErr != nil {
					return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32000, "message": searchErr.Error()}}
				}
				uids := ids.AllUIDs()
				if len(uids) == 0 {
					return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{"content": []map[string]interface{}{{"type": "text", "text": "No unread emails found."}}}}
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

			return JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]interface{}{
					"content": []map[string]interface{}{{"type": "text", "text": summary.String()}},
				},
			}

		case "read_email":
			seqStr, _ := params.Arguments["seq"].(string)
			seqNum, err := strconv.ParseUint(seqStr, 10, 32)
			if err != nil {
				return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32000, "message": "Invalid sequence number"}}
			}

			c, err := getIMAPClient()
			if err != nil {
				return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32000, "message": err.Error()}}
			}

			if _, err := c.Select("INBOX", nil).Wait(); err != nil {
				return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32000, "message": err.Error()}}
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

			return JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]interface{}{
					"content": []map[string]interface{}{{"type": "text", "text": bodyBuilder.String()}},
				},
			}

		case "send_email":
			to, _ := params.Arguments["to"].(string)
			subject, _ := params.Arguments["subject"].(string)
			body, _ := params.Arguments["body"].(string)

			smtpHost := os.Getenv("SMTP_HOST")
			smtpPort := os.Getenv("SMTP_PORT")
			user := os.Getenv("EMAIL_USER")
			pass := os.Getenv("EMAIL_PASS")

			auth := smtp.PlainAuth("", user, pass, smtpHost)
			addr := fmt.Sprintf("%s:%s", smtpHost, smtpPort)
			msg := []byte("From: " + user + "To: " + to + "\r\n" + "Subject: " + subject + "\r\n" + "\r\n" + body + "\r\n")

			if err := smtp.SendMail(addr, auth, user, []string{to}, msg); err != nil {
				return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32000, "message": err.Error()}}
			}

			return JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]interface{}{
					"content": []map[string]interface{}{{"type": "text", "text": fmt.Sprintf("Successfully sent email to %s", to)}},
				},
			}
		}
	}

	return JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Error: map[string]interface{}{
			"code":    -32601,
			"message": "Method not found",
		},
	}
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req JSONRPCRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			jsonLog("ERROR", "Malformed JSON request")
			continue
		}

		resp := handleRequest(req)
		bytes, _ := json.Marshal(resp)
		fmt.Println(string(bytes))
	}

	if err := scanner.Err(); err != nil {
		log.Fatalf("Stdin read error: %v", err)
	}
}
