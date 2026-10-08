package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
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

func mcpHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// Security: Validate Origin
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

	// Handle the 'initialize' method
	if req.Method == "initialize" {
		jsonLog("INFO", "Initialize requested")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]interface{}{},
				"serverInfo": map[string]interface{}{
					"name":    "hermes",
					"version": "1.0.0",
				},
			},
		})
		return
	}

	// DISCOVERABILITY: Handle tools/list
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
						"description": "Lists emails in the inbox. Can filter by read status.",
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
						"description": "Reads the body content of a specific email by ID.",
						"inputSchema": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"emailId": map[string]interface{}{
									"type":        "string",
									"description": "The unique identifier of the email.",
								},
							},
							"required": []string{"emailId"},
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

	// Handle the specific tool call
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

			emails := []map[string]interface{}{
				{"id": "1", "from": "alice@example.com", "subject": "Meeting Notes", "read": true},
				{"id": "2", "from": "bob@example.com", "subject": "Urgent: Project Update", "read": false},
				{"id": "3", "from": "charlie@example.com", "subject": "Hello!", "read": false},
			}

			var filtered []map[string]interface{}
			for _, e := range emails {
				if !unreadOnly || (unreadOnly && !e["read"].(bool)) {
					filtered = append(filtered, e)
				}
			}

			json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]interface{}{
					"content": []map[string]interface{}{
						{
							"type": "text",
							"text": fmt.Sprintf("Found %d emails: %v", len(filtered), filtered),
						},
					},
				},
			})
			return

		case "read_email":
			emailId, _ := params.Arguments["emailId"].(string)
			jsonLog("INFO", fmt.Sprintf("Tool 'read_email' called for ID: %s", emailId))

			content := "This is a dummy email body for email ID " + emailId + ". It contains some very important simulated information."
			json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]interface{}{
					"content": []map[string]interface{}{
						{
							"type": "text",
							"text": content,
						},
					},
				},
			})
			return

		case "send_email":
			to, _ := params.Arguments["to"].(string)
			subject, _ := params.Arguments["subject"].(string)
			jsonLog("INFO", fmt.Sprintf("Tool 'send_email' called to %s with subject '%s'", to, subject))

			json.NewEncoder(w).Encode(JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]interface{}{
					"content": []map[string]interface{}{
						{
							"type": "text",
							"text": fmt.Sprintf("Successfully sent email to %s", to),
						},
					},
				},
			})
			return
		}
	}

	// Default: Method not found
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
	// Use HERMES_PORT environment variable or default to 8080
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
