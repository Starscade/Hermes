package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"strings"
	"time"
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

// readUntilTag reads from the connection until a line starting with the expected tag is found
func readUntilTag(reader *bufio.Reader, conn net.Conn, tag string) (string, error) {
	var response strings.Builder
	for {
		// Set a deadline for each read attempt to prevent indefinite hanging
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))

		line, err := reader.ReadString('\n')
		if err != nil {
			return response.String(), err
		}
		response.WriteString(line)
		if strings.HasPrefix(line, tag) {
			break
		}
	}
	return response.String(), nil
}

// sendRawIMAPCommand implements a basic IMAP client with proper tag tracking and timeouts
func sendRawIMAPCommand(cmd string) (string, error) {
	host := os.Getenv("IMAP_HOST")
	port := os.Getenv("IMAP_PORT")
	user := os.Getenv("EMAIL_USER")
	pass := os.Getenv("EMAIL_PASS")

	if host == "" || port == "" {
		return "", fmt.Errorf("IMAP_HOST or IMAP_PORT not set")
	}

	// Use a dialer with a timeout to prevent hanging indefinitely
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.Dial("tcp", fmt.Sprintf("%s:%s", host, port))
	if err != nil {
		return "", err
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)

	// 1. Read greeting
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read IMAP greeting: %v", err)
	}

	// 2. Login
	fmt.Fprintf(conn, "a1 LOGIN %s %s\r\n", user, pass)
	resp, err := readUntilTag(reader, conn, "a1")
	if err != nil || !strings.Contains(resp, "OK") {
		return "", fmt.Errorf("IMAP login failed: %v", err)
	}

	// 3. Execute command(s)
	commands := strings.Split(cmd, "\r\n")
	var finalResponse string

	for i, c := range commands {
		if c == "" {
			continue
		}

		var currentTag string
		if strings.Contains(c, " ") {
			// If command already has a tag (e.g. "a1 SELECT"), use it
			parts := strings.SplitN(c, " ", 2)
			currentTag = parts[0]
			fmt.Fprintf(conn, "%s\r\n", c)
		} else {
			// Otherwise, assign a new tag
			currentTag = fmt.Sprintf("a%d", i+2)
			fmt.Fprintf(conn, "%s %s\r\n", currentTag, c)
		}

		resp, err = readUntilTag(reader, conn, currentTag)
		if err != nil {
			return "", err
		}
		finalResponse += resp
	}

	// 4. Logout
	fmt.Fprintf(conn, "a99 LOGOUT\r\n")

	return finalResponse, nil
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
							"sessionId": generateSessionID(), // Return a random string
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

			searchCmd := "a1 SELECT INBOX\r\na2 SEARCH ALL"
			if unreadOnly {
				searchCmd = "a1 SELECT INBOX\r\na2 SEARCH UNSEEN"
			}

			resp, err := sendRawIMAPCommand(searchCmd)
			if err != nil {
				json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32000, "message": err.Error()}})
				return
			}

			json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]interface{}{
					"content": []map[string]interface{}{
						{"type": "text", "text": fmt.Sprintf("IMAP Response: %s", resp)},
					},
				},
			})
			return

		case "read_email":
			seq, _ := params.Arguments["seq"].(string)
			jsonLog("INFO", fmt.Sprintf("Tool 'read_email' called for seq: %s", seq))

			cmd := fmt.Sprintf("a1 SELECT INBOX\r\na2 FETCH %s (RFC822)", seq)
			resp, err := sendRawIMAPCommand(cmd)
			if err != nil {
				json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: map[string]interface{}{"code": -32000, "message": err.Error()}})
				return
			}

			json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]interface{}{
					"content": []map[string]interface{}{
						{"type": "text", "text": resp},
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
						"description": "Lists email status. Uses raw IMAP SEARCH.",
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
