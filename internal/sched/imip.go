package sched

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

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

// SMTP sends iMIP messages through an SMTP relay. STARTTLS is used whenever
// the server offers it; credentials are only sent over TLS or to localhost.
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	// From is the envelope and header sender, e.g. "galdav <noreply@example.com>".
	From string
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
	addr := net.JoinHostPort(m.Host, fmt.Sprint(m.Port))
	var auth smtp.Auth
	if m.Username != "" {
		auth = smtp.PlainAuth("", m.Username, m.Password, m.Host)
	}
	return smtp.SendMail(addr, auth, from.Address, []string{msg.To}, body)
}

func header(v string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
}

func buildMIME(from string, msg Message) ([]byte, error) {
	rnd := make([]byte, 12)
	if _, err := rand.Read(rnd); err != nil {
		return nil, err
	}
	boundary := "galdav-" + hex.EncodeToString(rnd)
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
	h("Message-ID", "<"+hex.EncodeToString(rnd)+"@galdav>")
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
