package notifications

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"math/big"
	"net"
	"net/textproto"
	"os"
	osexec "os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func smtpTestCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-24 * time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func smtpTestCertificate(t *testing.T, ca *x509.Certificate, key *ecdsa.PrivateKey, variant string) tls.Certificate {
	t.Helper()
	leaf, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if variant == "wrong_host" {
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.2")}
	}
	if variant == "expired" {
		template.NotBefore, template.NotAfter = time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &leaf.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: leaf}
}

type smtpTLSReceipt struct {
	secure     bool
	auth, data int
	err        string
}

func smtpTLSSink(t *testing.T, mode string, certificate tls.Certificate) (int, <-chan smtpTLSReceipt) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	result, done := make(chan smtpTLSReceipt, 1), make(chan struct{})
	go func() {
		defer close(done)
		var receipt smtpTLSReceipt
		defer func() { result <- receipt }()
		conn, err := listener.Accept()
		_ = listener.Close()
		if err != nil {
			receipt.err = "accept failed"
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
		secure := func() bool {
			secured := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
			if err := secured.Handshake(); err != nil {
				return false
			}
			conn, receipt.secure = secured, true
			return true
		}
		if mode == "tls" && !secure() {
			return
		}
		client := textproto.NewConn(conn)
		if err := client.PrintfLine("220 owned TLS SMTP"); err != nil {
			return
		}
		for {
			line, err := client.ReadLine()
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO "):
				if receipt.secure {
					err = client.PrintfLine("250-owned\r\n250 AUTH PLAIN")
				} else {
					err = client.PrintfLine("250-owned\r\n250 STARTTLS")
				}
			case line == "STARTTLS" && mode == "starttls" && !receipt.secure:
				if client.PrintfLine("220 begin TLS") != nil || !secure() {
					return
				}
				client = textproto.NewConn(conn)
			case strings.HasPrefix(line, "AUTH "):
				receipt.auth++
				auth, decodeErr := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
				if !receipt.secure || decodeErr != nil || string(auth) != "\x00owned-user\x00owned-TLS-password" {
					receipt.err = "unexpected authentication or authentication before TLS"
					return
				}
				err = client.PrintfLine("235 authenticated")
			case line == "MAIL FROM:<sender@owned.test>", line == "RCPT TO:<admin@owned.test>":
				if !receipt.secure || receipt.auth != 1 {
					receipt.err = "envelope before authenticated TLS"
					return
				}
				err = client.PrintfLine("250 OK")
			case line == "DATA":
				receipt.data++
				if !receipt.secure || receipt.auth != 1 || client.PrintfLine("354 continue") != nil {
					receipt.err = "DATA before authenticated TLS"
					return
				}
				message, readErr := io.ReadAll(client.DotReader())
				if readErr != nil || !bytes.Contains(message, []byte("To: admin@owned.test")) || !bytes.Contains(message, []byte("Sub-Pool channel test")) || bytes.Contains(message, []byte("owned-TLS-password")) {
					receipt.err = "incorrect or secret-bearing message"
					return
				}
				err = client.PrintfLine("250 accepted")
			default:
				receipt.err = "unexpected SMTP command"
				return
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("TLS sink did not stop")
		}
	})
	return port, result
}

func TestSMTPVerifiedTLS(t *testing.T) {
	if os.Getenv("SUB_POOL_TEST_DATABASE_URL") == "" {
		t.Skip("PostgreSQL integration requires SUB_POOL_TEST_DATABASE_URL")
	}
	if os.Getenv("SUB_POOL_SMTP_TLS_TEST_CHILD") != "1" {
		// Fallback roots are process-global; isolate them instead of changing application or OS trust.
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		command := osexec.CommandContext(ctx, os.Args[0], "-test.run=^TestSMTPVerifiedTLS$", "-test.v")
		command.Env = append(os.Environ(), "SUB_POOL_SMTP_TLS_TEST_CHILD=1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("TLS subprocess failed: %v\n%s", err, output)
		}
		for _, mode := range []string{"tls", "starttls"} {
			for _, variant := range []string{"valid", "wrong_host", "unknown_ca", "expired"} {
				if !bytes.Contains(output, []byte("--- PASS: TestSMTPVerifiedTLS/"+mode+"/"+variant+" ")) {
					t.Fatalf("TLS subprocess did not verify %s/%s\n%s", mode, variant, output)
				}
			}
		}
		t.Log("8 verified TLS/authentication/receipt cases passed in the isolated subprocess")
		return
	}
	ca, key := smtpTestCA(t)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",x509usefallbackroots=1")
	x509.SetFallbackRoots(roots)
	for _, mode := range []string{"tls", "starttls"} {
		for _, variant := range []string{"valid", "wrong_host", "unknown_ca", "expired"} {
			t.Run(mode+"/"+variant, func(t *testing.T) {
				issuer, signer := ca, key
				if variant == "unknown_ca" {
					issuer, signer = smtpTestCA(t)
				}
				port, observed := smtpTLSSink(t, mode, smtpTestCertificate(t, issuer, signer, variant))
				base, token := setup(t)
				s, err := New(base.pool, base.auth, bytes.Repeat([]byte{31}, 32), false)
				if err != nil {
					t.Fatal(err)
				}
				in := input(config(t, s, token))
				in.Enabled, in.From, in.Recipient = true, "sender@owned.test", "admin@owned.test"
				in.SMTP = SMTP{"127.0.0.1", port, mode, "owned-user"}
				in.CredentialAction, in.Credential = "replace", "owned-TLS-password"
				configuration := save(t, s, token, in)
				n := request(t, s, token, configuration.Revision, 1)
				if n.State != "queued" || !process(t, s) {
					t.Fatal("TLS intent not processed")
				}
				got, err := s.Get(context.Background(), token, n.ID)
				wantState, wantReason := "failed", "TRANSPORT_UNAVAILABLE"
				if variant == "valid" {
					wantState, wantReason = "accepted", "TRANSPORT_ACCEPTED"
				}
				if err != nil || got.State != wantState || got.Reason != wantReason || got.FinishedAt == nil {
					t.Fatal("incorrect TLS delivery receipt", got.State, got.Reason, err)
				}
				select {
				case receipt := <-observed:
					if receipt.err != "" || variant == "valid" && (!receipt.secure || receipt.auth != 1 || receipt.data != 1) || variant != "valid" && (receipt.auth != 0 || receipt.data != 0) {
						t.Fatal("unsafe TLS exchange", receipt)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("missing TLS sink receipt")
				}
				replacement, err := New(s.pool, s.auth, bytes.Repeat([]byte{31}, 32), false)
				if err != nil {
					t.Fatal(err)
				}
				reloaded, err := replacement.Get(context.Background(), token, n.ID)
				if err != nil || !reflect.DeepEqual(reloaded, got) || !reflect.DeepEqual(request(t, replacement, token, configuration.Revision, 1), got) || process(t, replacement) {
					t.Fatal("TLS receipt changed or test was replayed after replacement", err)
				}
				t.Log("verified certificate policy, authenticated delivery boundary, durable receipt and no replay on port " + strconv.Itoa(port))
			})
		}
	}
}
