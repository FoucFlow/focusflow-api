// Package mailer sends HTML email over SMTP. Production uses the existing
// Zoho account (smtp.zoho.com:465, implicit TLS); port 587 uses STARTTLS;
// an empty host logs the message instead (local development).
package mailer

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"
)

type Mailer interface {
	Send(to, subject, html string) error
}

type SMTP struct {
	Host, User, Password, From, FromName string
	Port                                 int
}

func New(host string, port int, user, password, from, fromName string) Mailer {
	if host == "" {
		return logOnly{}
	}
	if from == "" {
		from = user
	}
	return &SMTP{Host: host, Port: port, User: user, Password: password, From: from, FromName: fromName}
}

func (m *SMTP) Send(to, subject, html string) error {
	addr := net.JoinHostPort(m.Host, fmt.Sprint(m.Port))
	msg := buildMessage(m.FromName, m.From, to, subject, html)

	var conn net.Conn
	var err error
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	if m.Port == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: m.Host})
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()

	if m.Port != 465 {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: m.Host}); err != nil {
				return fmt.Errorf("smtp starttls: %w", err)
			}
		}
	}
	if m.User != "" {
		if err := c.Auth(smtp.PlainAuth("", m.User, m.Password, m.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(m.From); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func buildMessage(fromName, from, to, subject, html string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s <%s>\r\n", mime.QEncoding.Encode("utf-8", fromName), from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/html; charset=\"UTF-8\"\r\n\r\n")
	b.WriteString(html)
	return []byte(b.String())
}

type logOnly struct{}

func (logOnly) Send(to, subject, html string) error {
	slog.Info("mail (SMTP not configured, not sent)", "to", to, "subject", subject, "body", html)
	return nil
}
