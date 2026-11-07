package notifications

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"net/textproto"
	"regexp"
	"strconv"
	"time"
)

type delivery struct {
	state, reason string
	providerID    *string
}

var providerID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

func newMailClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ResponseHeaderTimeout: 8 * time.Second, MaxResponseHeaderBytes: 16 << 10}}
}
func (s *Service) send(ctx context.Context, c storedConfig, secret string, n storedNotification) delivery {
	if c.Channel == "smtp" {
		return sendSMTP(ctx, c, secret, n)
	}
	body, e := json.Marshal(struct {
		From    string   `json:"from"`
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		Text    string   `json:"text"`
	}{c.From, []string{c.Recipient}, n.details.Subject, n.details.Body})
	if e != nil {
		return delivery{state: "failed", reason: "MESSAGE_INVALID"}
	}
	r, e := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(body))
	if e != nil {
		return delivery{state: "failed", reason: "MESSAGE_INVALID"}
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+secret)
	resp, e := s.client.Do(r)
	if e != nil {
		return delivery{state: "unknown", reason: "TRANSPORT_OUTCOME_UNKNOWN"}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return delivery{state: "failed", reason: "REMOTE_REJECTED"}
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return delivery{state: "unknown", reason: "REMOTE_PROTOCOL_UNKNOWN"}
	}
	data, e := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if e != nil || len(data) > 64<<10 {
		return delivery{state: "unknown", reason: "REMOTE_PROTOCOL_UNKNOWN"}
	}
	fields, e := objectFields(data, "id")
	var id string
	if e != nil || json.Unmarshal(fields["id"], &id) != nil || !providerID.MatchString(id) {
		return delivery{state: "unknown", reason: "REMOTE_PROTOCOL_UNKNOWN"}
	}
	return delivery{state: "accepted", reason: "TRANSPORT_ACCEPTED", providerID: &id}
}
func smtpError(e error, ambiguous bool) delivery {
	var rejection *textproto.Error
	if errors.As(e, &rejection) && rejection.Code >= 400 && rejection.Code < 600 {
		return delivery{state: "failed", reason: "REMOTE_REJECTED"}
	}
	if ambiguous {
		return delivery{state: "unknown", reason: "TRANSPORT_OUTCOME_UNKNOWN"}
	}
	return delivery{state: "failed", reason: "TRANSPORT_UNAVAILABLE"}
}
func sendSMTP(ctx context.Context, c storedConfig, secret string, n storedNotification) delivery {
	address := net.JoinHostPort(c.SMTP.Host, strconv.Itoa(c.SMTP.Port))
	conn, e := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", address)
	if e != nil {
		return smtpError(e, false)
	}
	defer conn.Close()
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if e = conn.SetDeadline(deadline); e != nil {
		return smtpError(e, false)
	}
	raw := conn
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()
	config := &tls.Config{ServerName: c.SMTP.Host, MinVersion: tls.VersionTLS12}
	if c.SMTP.TLS == "tls" {
		secured := tls.Client(conn, config)
		if e = secured.HandshakeContext(ctx); e != nil {
			return smtpError(e, false)
		}
		conn = secured
	}
	client, e := smtp.NewClient(conn, c.SMTP.Host)
	if e != nil {
		return smtpError(e, false)
	}
	defer client.Close()
	if c.SMTP.TLS == "starttls" {
		if e = client.StartTLS(config); e != nil {
			return smtpError(e, false)
		}
	}
	if c.SMTP.Username != "" {
		if e = client.Auth(smtp.PlainAuth("", c.SMTP.Username, secret, c.SMTP.Host)); e != nil {
			return smtpError(e, false)
		}
	}
	if e = client.Mail(c.From); e != nil {
		return smtpError(e, false)
	}
	if e = client.Rcpt(c.Recipient); e != nil {
		return smtpError(e, false)
	}
	writer, e := client.Data()
	if e != nil {
		return smtpError(e, false)
	}
	message := "From: " + c.From + "\r\nTo: " + c.Recipient + "\r\nSubject: " + mime.QEncoding.Encode("utf-8", n.details.Subject) + "\r\nMessage-ID: <notification-" + n.ID + "@sub-pool.local>\r\nDate: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + n.details.Body + "\r\n"
	if _, e = io.WriteString(writer, message); e != nil {
		return smtpError(e, true)
	}
	if e = writer.Close(); e != nil {
		return smtpError(e, true)
	}
	// Positive DATA completion is the receipt; a later QUIT failure cannot revoke acceptance.
	return delivery{state: "accepted", reason: "TRANSPORT_ACCEPTED"}
}
