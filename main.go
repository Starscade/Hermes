package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// LogEntry defines the JSON structure for console logs
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
	fmt.Fprintln(os.Stdout, string(bytes))
}

// JSONRPCRequest represents the incoming MCP request body
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// JSONRPCResponse represents the outgoing MCP response
type JSONRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   interface{} `json:"error,omitempty"`
}

// Global IMAP client to prevent "Unexpected EOF" caused by aggressive re-login
var (
	imapClient *imapclient.Client
	imapMutex  sync.Mutex
)

func getIMAPClient() (*imapclient.Client, error) {
	imapMutex.Lock()
	defer imapMutex.Unlock()

	// If client exists, try to verify it's still alive with a NOOP
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

func mcpHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	origin := r.Header.Get("Origin")
	if origin != "" && origin != "http://localhost" && origin != "https://localhost" {
		jsonLog("WARN", fmt.Sprintf("Rejected invalid origin: %s", origin))
		w.WriteHeader(http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var req JSONRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		jsonLog("ERROR", "Malformed JSON body")
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if req.Method == "server/discover" {
		jsonLog("INFO", "Discovery requested")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"sessionId": 1,
			},
		})
		return
	}

	if req.Method == "tools/call" {
		var params struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		json.Unmarshal(req.Params, &params)

		w.Header().Set("Content-Type", "application/json")

		switch params.Name {
		case "list_emails":
			unreadOnly, _ := params.Arguments["unreadOnly"].(bool)
			jsonLog("INFO", fmt.Sprintf("Tool 'list_emails' called (unreadOnly: %v)", unreadOnly))

			var c *imapclient.Client
			var err error

			for i := 0; i < 2; i++ {
				c, err = getIMAPClient()
				if err != nil {
					jsonLog("WARN", fmt.Sprintf("IMAP connection attempt %d failed: %v", i+1, err))
					time.Sleep(500 * time.Millisecond)
					continue
				}

				mbox, selectErr := c.Select("INBOX", nil).Wait()
				if selectErr == nil {
					var seqSet imap.SeqSet

					if unreadOnly {
						criteria := &imap.SearchCriteria{
							NotFlag: []imap.Flag{"\\Seen"},
						}
						ids, searchErr := c.Search(criteria, nil).Wait()
						if searchErr != nil {
							jsonLog("ERROR", fmt.Sprintf("list_emails search error: %v", searchErr))
							json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32000, "message": searchErr.Error()}})
							return
						}
						if len(ids.AllUIDs()) == 0 {
							json.NewEncoder(w).Encode(JSONRPCResponse{
								JSONRPC: "2.0",
								ID:      req.ID,
								Result: map[string]interface{}{
									"content": []map[string]interface{}{{"type": "text", "text": "No unread emails found."}},
								},
							})
							return
						}
						uids := ids.AllUIDs()
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

					options := &imap.FetchOptions{
						Envelope: true,
					}

					fetchCmd := c.Fetch(seqSet, options)
					defer fetchCmd.Close()

					var summary strings.Builder
					summary.WriteString(fmt.Sprintf("Inbox Summary (Total: %d):\n", mbox.NumMessages))

					count := 0
					msg := fetchCmd.Next()
					for msg != nil {
						count++
						item := msg.Next()
						if envItem, ok := item.(imapclient.FetchItemDataEnvelope); ok {
							env := envItem.Envelope
							from := "Unknown"
							if len(env.From) > 0 {
								from = env.From[0].Addr()
							}
							summary.WriteString(fmt.Sprintf("[%d] From: %s | Date: %s | Subject: %s\n",
								msg.SeqNum, from, env.Date.Format("2006-01-02 15:04"), env.Subject))
						}
						msg = fetchCmd.Next()
					}

					if count == 0 {
						summary.WriteString("No emails found matching criteria.")
					}

					json.NewEncoder(w).Encode(JSONRPCResponse{
						JSONRPC: "2.0",
						ID:      req.ID,
						Result: map[string]interface{}{
							"content": []map[string]interface{}{
								{"type": "text", "text": summary.String()},
							},
						},
					})
					return
				}

				imapMutex.Lock()
				imapClient = nil
				imapMutex.Unlock()
				jsonLog("WARN", fmt.Sprintf("IMAP select attempt %d failed: %v", i+1, selectErr))
				time.Sleep(500 * time.Millisecond)
			}

			jsonLog("ERROR", "list_emails failed after retries")
			json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32000, "message": "Email server connection failure"}})
			return

		case "read_email":
			seqStr, _ := params.Arguments["seq"].(string)
			jsonLog("INFO", fmt.Sprintf("Tool 'read_email' called for seq: %s", seqStr))

			seqNum, err := strconv.ParseUint(seqStr, 10, 32)
			if err != nil {
				jsonLog("ERROR", fmt.Sprintf("read_email invalid sequence number: %s", seqStr))
				json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32000, "message": "Invalid sequence number"}})
				return
			}

			c, err := getIMAPClient()
			if err != nil {
				jsonLog("ERROR", fmt.Sprintf("read_email IMAP client error: %v", err))
				json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32000, "message": err.Error()}})
				return
			}

			if _, err := c.Select("INBOX", nil).Wait(); err != nil {
				jsonLog("ERROR", fmt.Sprintf("read_email select error: %v", err))
				json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32000, "message": err.Error()}})
				return
			}

			seqSet := imap.SeqSetNum(uint32(seqNum))
			section := &imap.FetchItemBodySection{}
			options := &imap.FetchOptions{
				BodySection: []*imap.FetchItemBodySection{section},
			}

			fetchCmd := c.Fetch(seqSet, options)
			defer fetchCmd.Close()

			var bodyBuilder string
			msg := fetchCmd.Next()
			if msg != nil {
				for {
					item := msg.Next()
					if item == nil {
						break
					}
					if data, ok := item.(imapclient.FetchItemDataBodySection); ok {
						b, _ := io.ReadAll(data.Literal)
						bodyBuilder += string(b)
					}
				}
			}

			json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]interface{}{
					"content": []map[string]interface{}{
						{"type": "text", "text": bodyBuilder},
					},
				},
			})
			return

		case "send_email":
			to, _ := params.Arguments["to"].(string)
			subject, _ := params.Arguments["subject"].(string)
			body, _ := params.Arguments["body"].(string)
			jsonLog("INFO", fmt.Sprintf("Tool 'send_email' called to %s", to))

			smtpHost := os.Getenv("SMTP_HOST")
			smtpPort := os.Getenv("SMTP_PORT")
			user := os.Getenv("EMAIL_USER")
			pass := os.Getenv("EMAIL_PASS")

			auth := smtp.PlainAuth("", user, pass, smtpHost)
			addr := fmt.Sprintf("%s:%s", smtpHost, smtpPort)

			msg := []byte("To: " + to + "\r\n" +
				"Subject: " + subject + "\r\n" +
				"\r\n" +
				body + "\r\n")

			err := smtp.SendMail(addr, auth, user, []string{to}, msg)
			if err != nil {
				jsonLog("ERROR", fmt.Sprintf("send_email SMTP error: %v", err))
				json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32000, "message": err.Error()}})
				return
			}

			json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]interface{}{
					"content": []map[string]interface{}{
						{"type": "text", "text": fmt.Sprintf("Successfully sent email to %s", to)},
					},
				},
			})
			return
		}
	}

	if req.Method == "tools/list" {
		jsonLog("INFO", "Tools discovery requested")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(JSONRPCResponse{
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
		})
		return
	}

	jsonLog("WARN", fmt.Sprintf("Method not found: %s", req.Method))
	w.WriteHeader(http.StatusNotFound)
	json.NewEncoder(w).Encode(JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Error: map[string]interface{}{
			"code":    -32601,
			"message": "Method not found",
		},
	})
}

func main() {
	port := os.Getenv("HERMES_PORT")
	if port == "" {
		port = "8080"
	}
	address := fmt.Sprintf(":%s", port)

	mux := http.NewServeMux()
	mux.HandleFunc("/", mcpHandler)

	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", address, err)
	}

	server := &http.Server{
		Handler: mux,
	}

	jsonLog("INFO", fmt.Sprintf("MCP server starting on port %s", port))
	if err := server.Serve(listener); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
