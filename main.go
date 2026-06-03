package main

import (
	"crypto/tls"
	"flag"
	"log"
	"os"
	"strconv"

	"gopkg.in/gomail.v2"
)

func main() {
	smtpUser := os.Getenv("HERMES_USER")
	smtpPass := os.Getenv("HERMES_PASS")

	fromAddress := os.Getenv("HERMES_FROM")

	smtpHost := os.Getenv("HERMES_HOST")
	smtpPort := os.Getenv("HERMES_PORT")

	if smtpUser == "" || smtpPass == "" || fromAddress == "" {
		log.Fatal("Missing SMTP user, password, or from address.")
	}

	if fromAddress == "" {
		fromAddress = smtpUser
	}

	if smtpHost == "" {
		smtpHost = "smtp.gmail.com"
	}

	if smtpPort == "" {
		smtpPort = "587"
	}

	port, err := strconv.Atoi(smtpPort)
	if err != nil {
		log.Fatal(err)
	}

	toAddress := flag.String("to", "", "Recipient address.")
	mailSubject := flag.String("subject", "", "Email subject line.")
	mailBody := flag.String("body", "", "HTML message body.")

	flag.Parse()

	if *mailBody == "" {
		log.Fatal("Cannot send a blank email.")
	}

	if *toAddress == "" {
		*toAddress = fromAddress
	}

	m := gomail.NewMessage()
	m.SetHeader("From", fromAddress)
	m.SetHeader("To", *toAddress)
	m.SetHeader("Subject", *mailSubject)
	m.SetBody("text/html", *mailBody)

	d := gomail.NewDialer(smtpHost, port, smtpUser, smtpPass)

	d.TLSConfig = &tls.Config{
		InsecureSkipVerify: false,
		ServerName:         smtpHost,
	}

	if err := d.DialAndSend(m); err != nil {
		log.Fatal(err)
	}
}
