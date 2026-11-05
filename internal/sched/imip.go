package sched

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

const dateFormatStringMessageId = "2006.01.02.15.04.05"

// Message is an outgoing iMIP (RFC 6047) message.
type Message struct {
	To, From string // bare email addresses; From is the scheduling sender
	Method   string // REQUEST, REPLY or CANCEL
	Summary  string
	ICS      []byte
}

// Mailer delivers iMIP messages.
type Mailer interface {
	Send(Message) error
}

// SMTP sends iMIP messages through an SMTP relay. Port 465 uses implicit TLS;
// on other ports STARTTLS is used whenever the server offers it. Credentials
// are only sent over TLS or to localhost.
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	// From is the envelope and header sender, e.g. "whitegopher <noreply@example.com>".
	From string

	// Test hooks: a custom TLS config, and implicit TLS on a port other than 465.
	tlsConfig   *tls.Config
	implicitTLS bool
}

var subjects = map[string]string{"REQUEST": "Invitation", "CANCEL": "Cancelled", "REPLY": "Reply"}

func (m *SMTP) Send(msg Message) error {
	from, err := mail.ParseAddress(m.From)
	if err != nil {
		return fmt.Errorf("smtp.from: %w", err)
	}
	body, err := buildMIME(from.String(), msg)
	if err != nil {
		return err
	}
	return m.deliver(from.Address, []string{msg.To}, body)
}

// deliver sends one message. net/smtp.SendMail cannot do implicit TLS, so the
// session is driven by hand, with timeouts so a stuck relay cannot block callers.
func (m *SMTP) deliver(from string, to []string, body []byte) error {
	cfg := m.tlsConfig
	if cfg == nil {
		cfg = &tls.Config{ServerName: m.Host}
	}
	implicit := m.Port == 465 || m.implicitTLS
	addr := net.JoinHostPort(m.Host, fmt.Sprint(m.Port))
	d := net.Dialer{Timeout: 30 * time.Second}
	var conn net.Conn
	var err error
	if implicit {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, cfg)
	} else {
		conn, err = d.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	conn.SetDeadline(time.Now().Add(2 * time.Minute))
	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if !implicit {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(cfg); err != nil {
				return err
			}
		}
	}
	if m.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", m.Username, m.Password, m.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, r := range to {
		if err := c.Rcpt(r); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// SendText sends a plain-text message, e.g. a password-reset link.
func (m *SMTP) SendText(to, subject, text string) error {
	from, err := mail.ParseAddress(m.From)
	if err != nil {
		return fmt.Errorf("smtp.from: %w", err)
	}
	rcpt, err := mail.ParseAddress(to)
	if err != nil {
		return fmt.Errorf("recipient: %w", err)
	}
	rnd := make([]byte, 12)
	if _, err := rand.Read(rnd); err != nil {
		return err
	}
	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + header(v) + "\r\n") }
	h("From", from.String())
	h("To", rcpt.Address)
	h("Subject", (&mimeWord{}).encode(subject))
	h("Date", time.Now().Format(time.RFC1123Z))
	h("Message-ID", "<"+time.Now().Format(dateFormatStringMessageId)+"."+hex.EncodeToString(rnd)+"@whitegopher>")
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=UTF-8")
	h("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	w := quotedprintable.NewWriter(&b)
	w.Write([]byte(strings.ReplaceAll(text, "\n", "\r\n")))
	w.Close()
	return m.deliver(from.Address, []string{rcpt.Address}, b.Bytes())
}

func header(v string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
}

func buildMIME(from string, msg Message) ([]byte, error) {
	rnd := make([]byte, 12)
	if _, err := rand.Read(rnd); err != nil {
		return nil, err
	}
	boundary := "whitegopher-" + hex.EncodeToString(rnd)
	subject := subjects[msg.Method]
	if subject == "" {
		subject = msg.Method
	}
	if msg.Summary != "" {
		subject += ": " + msg.Summary
	}
	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + header(v) + "\r\n") }
	h("From", from)
	h("To", msg.To)
	if msg.From != "" {
		h("Reply-To", msg.From)
	}
	h("Subject", (&mimeWord{}).encode(subject))
	h("Date", time.Now().Format(time.RFC1123Z))
	h("Message-ID", "<"+time.Now().Format(dateFormatStringMessageId)+"."+hex.EncodeToString(rnd)+"@whitegopher>")
	h("MIME-Version", "1.0")
	h("Content-Type", "multipart/alternative; boundary="+boundary)
	b.WriteString("\r\n")

	part := func(ct string, body []byte) {
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: " + ct + "\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
		w := quotedprintable.NewWriter(&b)
		w.Write(body)
		w.Close()
		b.WriteString("\r\n")
	}
	part("text/plain; charset=UTF-8", []byte(subject+"\r\n\r\nThis message carries a calendar invitation; open it in a calendar application.\r\n"))
	part("text/calendar; charset=UTF-8; method="+msg.Method, msg.ICS)
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes(), nil
}

// mimeWord encodes non-ASCII header text (RFC 2047).
type mimeWord struct{}

func (mimeWord) encode(s string) string {
	for _, r := range s {
		if r > 126 || r < 32 {
			return "=?UTF-8?q?" + qEncode(s) + "?="
		}
	}
	return s
}

func qEncode(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c == ' ':
			b.WriteByte('_')
		case c > 32 && c < 127 && c != '=' && c != '?' && c != '_':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "=%02X", c)
		}
	}
	return b.String()
}
