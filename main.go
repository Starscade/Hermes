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
	fromAddress := os.Getenv("HERMES_FROM")
	smtpHost := os.Getenv("HERMES_HOST")
	smtpPass := os.Getenv("HERMES_PASS")
	smtpPort := os.Getenv("HERMES_PORT")

	if fromAddress == "" || smtpHost == "" || smtpPass == "" {
		log.Fatal("Missing sender address, SMTP host, or password.")
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

	if smtpPort == "" {
		smtpPort = "587"
	}

	m := gomail.NewMessage()
	m.SetHeader("From", fromAddress)
	m.SetHeader("To", *toAddress)
	m.SetHeader("Subject", *mailSubject)
	m.SetBody("text/html", *mailBody)

	port, err := strconv.Atoi(smtpPort)
	if err != nil {
		log.Fatal(err)
	}

	d := gomail.NewDialer(smtpHost, port, fromAddress, smtpPass)
	if err := d.DialAndSend(m); err != nil {
		log.Fatal(err)
	}

	d.TLSConfig = &tls.Config{
		InsecureSkipVerify: false,
		ServerName:         smtpHost,
	}

}
