package main

import (
	"crypto/tls"
	"flag"
	"io"
	"log"
	"os"
	"strconv"
	"strings"

	"gopkg.in/gomail.v2"
)

func main() {
	smtpUser := os.Getenv("HERMES_USER")
	smtpPass := os.Getenv("HERMES_PASS")

	fromAddress := os.Getenv("HERMES_FROM")

	smtpHost := os.Getenv("HERMES_HOST")
	smtpPort := os.Getenv("HERMES_PORT")

	if smtpUser == "" || smtpPass == "" {
		log.Fatal("Missing SMTP user or password.")
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

	toAddress := flag.String("to", "", "Recipient addresse(s).")
	mailSubject := flag.String("subject", "", "Email subject line.")

	flag.Parse()

	mailBody, err := getStdin()
	if err != nil {
		log.Fatal(err)
	}
	if strings.TrimSpace(mailBody) == "" {
		log.Fatal("Cannot send a blank email.")
	}

	recipients := []string{}
	if *toAddress != "" {
		recipients = strings.Split(*toAddress, ",")
	} else {
		recipients = []string{fromAddress}
	}

	m := gomail.NewMessage()
	m.SetHeader("From", fromAddress)
	m.SetHeader("To", recipients...)
	m.SetHeader("Subject", *mailSubject)
	m.SetBody("text/html", mailBody)

	d := gomail.NewDialer(smtpHost, port, smtpUser, smtpPass)

	d.TLSConfig = &tls.Config{
		InsecureSkipVerify: false,
		ServerName:         smtpHost,
	}

	if err := d.DialAndSend(m); err != nil {
		log.Fatal(err)
	}
}

func getStdin() (string, error) {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}

	var input []byte

	if (stat.Mode() & os.ModeCharDevice) != 0 {
		return "", nil
	}

	input, err = io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}

	return string(input), nil
}
