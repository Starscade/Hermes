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
	From     string `json:"from"`
	Subject  string `json:"subject"`
	Date     string `json:"date"`
	Body     string `json:"body"`
	MimeType string `json:"mime_type"`
}

type SendRequest struct {
	To      []string `json:"to"`
	Cc      []string `json:"cc,omitempty"`
	Bcc     []string `json:"bcc,omitempty"`
	Subject string   `json:"subject"`
	Body    string   `json:"body"`
}

func minifyHTML(html string) string {
	re := regexp.MustCompile(`>\s+<`)
	return re.ReplaceAllString(html, "><")
}

func fetchUnread(user, pass string) ([]Email, error) {
	host := os.Getenv("HERMES_HOST_IMAP")
	port := os.Getenv("HERMES_PORT_IMAP")
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

		body, mediaTypeFound := "", ""
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				continue
			}
			mediaType, _, err := mime.ParseMediaType(p.Header.Get("Content-Type"))
			if err != nil {
				continue
			}
			if mediaType == "text/plain" || mediaType == "text/html" {
				b, _ := io.ReadAll(p.Body)
				body, mediaTypeFound = string(b), mediaType
				break
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
				http.Error(w, err.Error(), http.StatusInternalServerError)
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
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			host := os.Getenv("HERMES_HOST_SMTP")
			port := os.Getenv("HERMES_PORT_SMTP")
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
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusAccepted)

		default:
			http.Error(w, "", http.StatusMethodNotAllowed)
		}
	})

	port := os.Getenv("HERMES_PORT")
	if port == "" {
		port = "8143"
	}
	fmt.Printf("\n  Serving on port: %s ...\n\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
