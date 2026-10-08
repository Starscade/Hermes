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

func getIMAPClient() (*imapclient.Client, error) {
	host := os.Getenv("IMAP_HOST")
	port := os.Getenv("IMAP_PORT")
	user := os.Getenv("EMAIL_USER")
	pass := os.Getenv("EMAIL_PASS")

	if host == "" || port == "" {
		return nil, fmt.Errorf("IMAP_HOST or IMAP_PORT not set")
	}

	c, err := imapclient.DialTLS(fmt.Sprintf("%s:%s", host, port), nil)
	if err != nil {
		return nil, err
	}

	if err := c.Login(user, pass).Wait(); err != nil {
		c.Logout().Wait()
		return nil, err
	}

	return c, nil
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

			c, err := getIMAPClient()
			if err != nil {
				json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32000, "message": err.Error()}})
				return
			}
			defer c.Logout().Wait()

			mbox, err := c.Select("INBOX", nil).Wait()
			if err != nil {
				json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32000, "message": err.Error()}})
				return
			}

			criteria := &imap.SearchCriteria{}
			if unreadOnly {
				// In go-imap v2, we use NotFlag to specify flags that the message must NOT have.
				// The flag for 'Seen' is typically "\Seen".
				criteria.NotFlag = []imap.Flag{"\\Seen"}
			}

			ids, err := c.Search(criteria, nil).Wait()
			if err != nil {
				json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32000, "message": err.Error()}})
				return
			}

			respText := fmt.Sprintf("Found %d emails. Sequence IDs: %v (Total messages in box: %d)", len(ids.AllUIDs()), ids.AllUIDs(), mbox.NumMessages)
			json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]interface{}{
					"content": []map[string]interface{}{
						{"type": "text", "text": respText},
					},
				},
			})
			return

		case "read_email":
			seqStr, _ := params.Arguments["seq"].(string)
			jsonLog("INFO", fmt.Sprintf("Tool 'read_email' called for seq: %s", seqStr))

			seqNum, err := strconv.ParseUint(seqStr, 10, 32)
			if err != nil {
				json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32000, "message": "Invalid sequence number"}})
				return
			}

			c, err := getIMAPClient()
			if err != nil {
				json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32000, "message": err.Error()}})
				return
			}
			defer c.Logout().Wait()

			if _, err := c.Select("INBOX", nil).Wait(); err != nil {
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
						"description": "Lists email status using imap library.",
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
	address := fmt.Sprintf("127.0.0.1:%s", port)

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", mcpHandler)

	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", address, err)
	}

	server := &http.Server{
		Handler: mux,
	}

	jsonLog("INFO", fmt.Sprintf("MCP server starting on http://%s/mcp", address))
	if err := server.Serve(listener); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
