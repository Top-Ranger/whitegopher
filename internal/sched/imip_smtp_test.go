package sched

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func testCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},

		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// fakeSMTP serves one SMTP session on conn and returns the DATA payload.
func fakeSMTP(conn net.Conn, starttls *tls.Config, got chan<- string) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	say := func(s string) { conn.Write([]byte(s + "\r\n")) }
	say("220 localhost ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			if starttls != nil {
				conn.Write([]byte("250-localhost\r\n250 STARTTLS\r\n"))
			} else {
				say("250 localhost")
			}
		case cmd == "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(conn, starttls)
			if tc.Handshake() != nil {
				return
			}
			conn, r, starttls = tc, bufio.NewReader(tc), nil
			say = func(s string) { tc.Write([]byte(s + "\r\n")) }
		case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
			say("250 ok")
		case cmd == "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			got <- b.String()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		}
	}
}

func testDeliver(t *testing.T, implicit bool) {
	cert, pool := testCert(t)
	srvCfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		if implicit {
			fakeSMTP(tls.Server(conn, srvCfg), nil, got)
		} else {
			fakeSMTP(conn, srvCfg, got)
		}
	}()
	m := &SMTP{Host: "localhost", Port: ln.Addr().(*net.TCPAddr).Port, tlsConfig: &tls.Config{ServerName: "localhost", RootCAs: pool}}
	if implicit {
		// The test listener cannot bind 465, so force the implicit path.
		m.implicitTLS = true
	}
	if err := m.deliver("a@example.com", []string{"b@example.com"}, []byte("Subject: hi\r\n\r\nbody\r\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case data := <-got:
		if !strings.Contains(data, "body") {
			t.Fatalf("unexpected DATA: %q", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server never received message")
	}
}

func TestDeliverImplicitTLS(t *testing.T) { testDeliver(t, true) }
func TestDeliverSTARTTLS(t *testing.T)    { testDeliver(t, false) }
