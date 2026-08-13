package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/smtp"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message/mail"
)

type Email struct {
	Body     string   `json:"body"`
	Cc       []string `json:"cc,omitempty"`
	Date     string   `json:"date"`
	From     string   `json:"from"`
	MimeType string   `json:"mime_type"`
	Subject  string   `json:"subject"`
	To       []string `json:"to,omitempty"`
}

type SendRequest struct {
	Bcc     []string `json:"bcc,omitempty"`
	Body    string   `json:"body"`
	Cc      []string `json:"cc,omitempty"`
	Subject string   `json:"subject,omitempty"`
	To      []string `json:"to"`
}

func logJSON(level, message string) {
	currentTime := time.Now().Format(time.RFC3339)
	fmt.Printf("{\"time\":\"%s\",\"level\":\"%s\",\"log\":\"%s\"}\n", currentTime, level, message)
}

func minifyHTML(html string) string {
	re := regexp.MustCompile(`>\s+<`)
	return re.ReplaceAllString(html, "><")
}

func fetchUnread(user, pass string) ([]Email, error) {
	host := os.Getenv("IMAP_HOST")
	port := os.Getenv("IMAP_PORT")
	if host == "" {
		host = "imap.gmail.com"
	}
	if port == "" {
		port = "993"
	}

	c, err := client.DialTLS(fmt.Sprintf("%s:%s", host, port), nil)
	if err != nil {
		return nil, err
	}
	defer c.Logout()

	if err := c.Login(user, pass); err != nil {
		return nil, err
	}
	if _, err := c.Select("INBOX", false); err != nil {
		return nil, err
	}

	criteria := imap.NewSearchCriteria()
	criteria.WithoutFlags = []string{imap.SeenFlag}
	ids, err := c.Search(criteria)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []Email{}, nil
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(ids...)
	section := &imap.BodySectionName{}
	items := []imap.FetchItem{imap.FetchEnvelope, section.FetchItem()}

	messages := make(chan *imap.Message, len(ids))
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

func main() {
	http.HandleFunc("/mail", func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
			http.Error(w, "", http.StatusUnauthorized)
			return
		}

		switch r.Method {
		case http.MethodGet:
			emails, err := fetchUnread(user, pass)
			if err != nil {
				logJSON("ERROR", err.Error())
				http.Error(w, "", http.StatusInternalServerError)
				return
			}
			if len(emails) == 0 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(emails)

		case http.MethodPost:
			var req SendRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				logJSON("ERROR", err.Error())
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			host := os.Getenv("SMTP_HOST")
			port := os.Getenv("SMTP_PORT")
			if host == "" {
				host = "smtp.gmail.com"
			}
			if port == "" {
				port = "587"
			}

			msg := []byte("From: " + user + "\r\n" +
				"To: " + strings.Join(req.To, ",") + "\r\n" +
				"Cc: " + strings.Join(req.Cc, ",") + "\r\n" +
				"Subject: " + req.Subject + "\r\n\r\n" +
				req.Body)

			auth := smtp.PlainAuth("", user, pass, host)
			err := smtp.SendMail(host+":"+port, auth, user, append(req.To, append(req.Cc, req.Bcc...)...), msg)
			if err != nil {
				logJSON("ERROR", err.Error())
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusAccepted)

		default:
			http.Error(w, "", http.StatusMethodNotAllowed)
		}
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	port := os.Getenv("HERMES_PORT")
	if port == "" {
		port = "8143"
	}
	logJSON("INFO", fmt.Sprintf("Serving on port %s...", port))
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
