// Copyright 2014 The Gogs Authors. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package mailer

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"os"
	"strings"
	"time"

	"gopkg.in/gomail.v2"

	"github.com/gogits/gogs/modules/log"
	"github.com/gogits/gogs/modules/setting"
)

type Message struct {
	Info string // Message information for log purpose.
	*gomail.Message
}

// NewMessageFrom creates new mail message object with custom From header.
func NewMessageFrom(to []string, from, subject, body string) *Message {
	msg := gomail.NewMessage()
	msg.SetHeader("From", from)
	msg.SetHeader("To", to...)
	msg.SetHeader("Subject", subject)
	msg.SetDateHeader("Date", time.Now())
	msg.SetBody("text/plain", body)
	msg.AddAlternative("text/html", body)

	return &Message{
		Message: msg,
	}
}

// NewMessage creates new mail message object with default From header.
func NewMessage(to []string, subject, body string) *Message {
	return NewMessageFrom(to, setting.MailService.From, subject, body)
}

type loginAuth struct {
	username, password string
}

// SMTP AUTH LOGIN Auth Handler
func LoginAuth(username, password string) smtp.Auth {
	return &loginAuth{username, password}
}

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	return "LOGIN", []byte{}, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if more {
		switch string(fromServer) {
		case "Username:":
			return []byte(a.username), nil
		case "Password:":
			return []byte(a.password), nil
		default:
			return nil, fmt.Errorf("unknwon fromServer: %s", string(fromServer))
		}
	}
	return nil, nil
}

type Sender struct {
}

func (s *Sender) Send(from string, to []string, msg io.WriterTo) error {
	opts := setting.MailService

	host, port, err := net.SplitHostPort(opts.Host)
	if err != nil {
		return err
	}

	tlsconfig := &tls.Config{
		InsecureSkipVerify: opts.SkipVerify,
		ServerName:         host,
	}

	if opts.UseCertificate {
		cert, err := tls.LoadX509KeyPair(opts.CertFile, opts.KeyFile)
		if err != nil {
			return err
		}
		tlsconfig.Certificates = []tls.Certificate{cert}
	}

	conn, err := net.Dial("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return err
	}
	defer conn.Close()

	isSecureConn := false
	// Start TLS directly if the port ends with 465 (SMTPS protocol)
	if strings.HasSuffix(port, "465") {
		conn = tls.Client(conn, tlsconfig)
		isSecureConn = true
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("NewClient: %v", err)
	}

	if !setting.MailService.DisableHelo {
		hostname := setting.MailService.HeloHostname
		if len(hostname) == 0 {
			hostname, err = os.Hostname()
			if err != nil {
				return err
			}
		}

		if err = client.Hello(hostname); err != nil {
			return fmt.Errorf("Hello: %v", err)
		}
	}

	// If not using SMTPS, alway use STARTTLS if available
	hasStartTLS, _ := client.Extension("STARTTLS")
	if !isSecureConn && hasStartTLS {
		if err = client.StartTLS(tlsconfig); err != nil {
			return fmt.Errorf("StartTLS: %v", err)
		}
	}

	canAuth, options := client.Extension("AUTH")
	if canAuth && len(opts.User) > 0 {
		var auth smtp.Auth

		if strings.Contains(options, "CRAM-MD5") {
			auth = smtp.CRAMMD5Auth(opts.User, opts.Passwd)
		} else if strings.Contains(options, "PLAIN") {
			auth = smtp.PlainAuth("", opts.User, opts.Passwd, host)
		} else if strings.Contains(options, "LOGIN") {
			// Patch for AUTH LOGIN
			auth = LoginAuth(opts.User, opts.Passwd)
		}

		if auth != nil {
			if err = client.Auth(auth); err != nil {
				return fmt.Errorf("Auth: %v", err)
			}
		}
	}

	if err = client.Mail(from); err != nil {
		return fmt.Errorf("Mail: %v", err)
	}

	for _, rec := range to {
		if err = client.Rcpt(rec); err != nil {
			return fmt.Errorf("Rcpt: %v", err)
		}
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("Data: %v", err)
	} else if _, err = msg.WriteTo(w); err != nil {
		return fmt.Errorf("WriteTo: %v", err)
	} else if err = w.Close(); err != nil {
		return fmt.Errorf("Close: %v", err)
	}

	return client.Quit()
}

func processMailQueue() {
	sender := &Sender{}

	for {
		select {
		case msg := <-mailQueue:
			log.Trace("New e-mail sending request %s: %s", msg.GetHeader("To"), msg.Info)
			if err := gomail.Send(sender, msg.Message); err != nil {
				log.Error(4, "Fail to send e-mails %s: %s - %v", msg.GetHeader("To"), msg.Info, err)
			} else {
				log.Trace("E-mails sent %s: %s", msg.GetHeader("To"), msg.Info)
			}
		}
	}
}

var mailQueue chan *Message

func NewContext() {
	if setting.MailService == nil {
		return
	}

	mailQueue = make(chan *Message, setting.MailService.QueueLength)
	go processMailQueue()
}

func SendAsync(msg *Message) {
	go func() {
		mailQueue <- msg
	}()
}
