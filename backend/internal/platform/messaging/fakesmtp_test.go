package messaging

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a minimal mail server for tests: EHLO, STARTTLS, AUTH PLAIN, MAIL, RCPT, DATA, QUIT.
type fakeSMTP struct {
	t        *testing.T
	ln       net.Listener
	cert     tls.Certificate
	pool     *x509.CertPool
	starttls bool   // offer STARTTLS
	implicit bool   // TLS from the first byte
	rcptCode int    // reply to RCPT (0 = 250)
	user     string // expected AUTH PLAIN user and password; "" accepts anything
	pass     string

	mu       sync.Mutex
	messages []string // DATA bodies
	sawTLS   bool
	sawAuth  string
	mailFrom string
	rcptTo   string
}

func newFakeSMTP(t *testing.T, configure func(*fakeSMTP)) *fakeSMTP {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	f := &fakeSMTP{t: t, starttls: true, cert: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool: x509.NewCertPool()}
	f.pool.AddCert(leaf)
	if configure != nil {
		configure(f)
	}
	if f.implicit {
		f.ln, err = tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{f.cert}})
	} else {
		f.ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.ln.Close() })
	go f.serve()
	return f
}

func (f *fakeSMTP) config() SMTPConfig {
	_, port, _ := net.SplitHostPort(f.ln.Addr().String())
	p, _ := strconv.Atoi(port)
	mode := TLSStartTLS
	if f.implicit {
		mode = TLSImplicit
	}
	return SMTPConfig{Host: "localhost", Port: p, From: "no-reply@shikshit.test", FromName: "Shikshit", TLS: mode,
		TLSConfig: &tls.Config{RootCAs: f.pool, MinVersion: tls.VersionTLS12}, Timeout: 5 * time.Second}
}

func (f *fakeSMTP) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(c)
	}
}

func (f *fakeSMTP) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	r, w := bufio.NewReader(c), bufio.NewWriter(c)
	say := func(s string) { _, _ = w.WriteString(s + "\r\n"); _ = w.Flush() }
	say("220 localhost fake")
	tlsOn := f.implicit
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch cmd {
		case "EHLO", "HELO":
			ext := []string{"250-localhost"}
			if f.starttls && !tlsOn {
				ext = append(ext, "250-STARTTLS")
			}
			ext = append(ext, "250 AUTH PLAIN")
			for _, e := range ext {
				_, _ = w.WriteString(e + "\r\n")
			}
			_ = w.Flush()
		case "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{f.cert}})
			if err := tc.Handshake(); err != nil {
				return
			}
			c, tlsOn = tc, true
			r, w = bufio.NewReader(tc), bufio.NewWriter(tc)
			f.mu.Lock()
			f.sawTLS = true
			f.mu.Unlock()
		case "AUTH":
			parts := strings.Fields(line)
			raw, _ := base64.StdEncoding.DecodeString(parts[len(parts)-1])
			fields := strings.Split(string(raw), "\x00")
			f.mu.Lock()
			f.sawAuth = string(raw)
			f.mu.Unlock()
			if f.user != "" && (len(fields) != 3 || fields[1] != f.user || fields[2] != f.pass) {
				say("535 bad credentials for someone@example.com")
				continue
			}
			say("235 ok")
		case "MAIL":
			f.mu.Lock()
			f.mailFrom = line
			f.mu.Unlock()
			say("250 ok")
		case "RCPT":
			f.mu.Lock()
			f.rcptTo = line
			f.mu.Unlock()
			if f.rcptCode != 0 {
				say(strconv.Itoa(f.rcptCode) + " no such user: " + line)
				continue
			}
			say("250 ok")
		case "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.messages = append(f.messages, b.String())
			f.mu.Unlock()
			say("250 queued")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func (f *fakeSMTP) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.messages...)
}
