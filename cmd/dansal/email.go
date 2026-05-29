package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// smtpObscure encrypts password with AES-256-GCM.
// Pass an existing keyHex to reuse the key; pass "" to generate a new one.
// Returns (base64 ciphertext, hex key).
func smtpObscure(password, keyHex string) (string, string, error) {
	var key []byte
	if keyHex == "" {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return "", "", err
		}
	} else {
		var err error
		key, err = hex.DecodeString(keyHex)
		if err != nil || len(key) != 32 {
			return "", "", fmt.Errorf("invalid password_key")
		}
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", "", err
	}
	ct := gcm.Seal(nonce, nonce, []byte(password), nil)
	return base64.StdEncoding.EncodeToString(ct), hex.EncodeToString(key), nil
}

// smtpReveal decrypts an AES-256-GCM ciphertext produced by smtpObscure.
func smtpReveal(encBase64, keyHex string) (string, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		return "", fmt.Errorf("invalid password_key")
	}
	ct, err := base64.StdEncoding.DecodeString(encBase64)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(ct) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	plain, err := gcm.Open(nil, ct[:gcm.NonceSize()], ct[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt failed")
	}
	return string(plain), nil
}

// generateMessageID returns a unique RFC 5322 Message-ID using the From domain.
func generateMessageID() string {
	domain := "localhost"
	if from := config.SMTP.From; strings.Contains(from, "@") {
		domain = from[strings.Index(from, "@")+1:]
	} else if h, err := os.Hostname(); err == nil {
		domain = h
	}
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("<%d.%x@%s>", time.Now().UnixNano(), b, domain)
}

// securityHeaders returns the common security/RFC-compliance headers for automated mail.
func securityHeaders(msgID, fromHeader, to, subject string) string {
	return "MIME-Version: 1.0\r\n" +
		"Date: " + time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 +0000") + "\r\n" +
		"Message-ID: " + msgID + "\r\n" +
		"From: " + fromHeader + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n" +
		"Auto-Submitted: auto-generated\r\n" +
		"Precedence: bulk\r\n" +
		"X-Auto-Response-Suppress: All\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n"
}

// sendMailLocal delivers a message by piping it to the configured sendmail binary.
func sendMailLocal(cfg SMTPConfig, to, subject, body string) (string, error) {
	from := cfg.From
	if from == "" {
		return "", fmt.Errorf("smtp.from is required when using sendmail")
	}
	fromHeader := from
	if cfg.FromName != "" {
		fromHeader = mime.QEncoding.Encode("utf-8", cfg.FromName) + " <" + from + ">"
	}
	msgID := generateMessageID()
	msg := securityHeaders(msgID, stripCRLF(fromHeader), stripCRLF(to), subject) + body
	cmd := exec.Command(cfg.Sendmail, "-t", "-i")
	cmd.Stdin = strings.NewReader(msg)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("sendmail: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return msgID, nil
}

// SendEmail sends a plain-text email and returns the generated Message-ID.
// If smtp.sendmail is set, the local MTA is used; otherwise the configured SMTP server is used.
func SendEmail(to, subject, body string) (string, error) {
	cfg := config.SMTP
	if cfg.Sendmail != "" {
		return sendMailLocal(cfg, to, subject, body)
	}
	if cfg.Host == "" {
		return "", fmt.Errorf("SMTP not configured")
	}

	port := cfg.Port
	if port == 0 {
		port = 587
	}

	timeout := time.Duration(cfg.TimeoutSecs) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	password := ""
	if cfg.Password != "" && cfg.PasswordKey != "" {
		var err error
		password, err = smtpReveal(cfg.Password, cfg.PasswordKey)
		if err != nil {
			return "", fmt.Errorf("SMTP password: %w", err)
		}
	}

	from := cfg.From
	if from == "" {
		from = cfg.Username
	}
	fromHeader := from
	if cfg.FromName != "" {
		fromHeader = mime.QEncoding.Encode("utf-8", cfg.FromName) + " <" + from + ">"
	}

	// Strip CRLFs from header fields to prevent email header injection.
	to = stripCRLF(to)
	fromHeader = stripCRLF(fromHeader)

	msgID := generateMessageID()
	msg := []byte(securityHeaders(msgID, fromHeader, to, subject) + body)

	portStr := strconv.Itoa(port)

	if cfg.TLS == "tls" {
		raw, err := dialSMTPConn(cfg.Host, portStr, timeout)
		if err != nil {
			return "", fmt.Errorf("TLS dial %s:%s: %w", cfg.Host, portStr, err)
		}
		raw.SetDeadline(time.Now().Add(timeout))
		tlsConn := tls.Client(raw, &tls.Config{ServerName: cfg.Host})
		if err := tlsConn.Handshake(); err != nil {
			raw.Close()
			return "", fmt.Errorf("TLS handshake %s: %w", cfg.Host, err)
		}
		defer tlsConn.Close()
		return msgID, smtpSend(tlsConn, cfg.Host, cfg.Username, password, from, to, msg)
	}

	conn, err := dialSMTPConn(cfg.Host, portStr, timeout)
	if err != nil {
		return "", fmt.Errorf("dial %s:%s: %w", cfg.Host, portStr, err)
	}
	conn.SetDeadline(time.Now().Add(timeout))
	defer conn.Close()

	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return "", fmt.Errorf("SMTP client: %w", err)
	}
	defer c.Quit()

	if cfg.TLS != "none" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
				return "", fmt.Errorf("STARTTLS: %w", err)
			}
		}
	}

	if cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, password, cfg.Host)); err != nil {
			return "", fmt.Errorf("SMTP auth: %w", err)
		}
	}
	return msgID, smtpDeliver(c, from, to, msg)
}

// dialSMTPConn resolves host to all its IP addresses and tries each in order,
// using a per-address timeout so a dead IPv6 address doesn't block the IPv4 fallback.
func dialSMTPConn(host, port string, timeout time.Duration) (net.Conn, error) {
	ips, err := net.LookupHost(host)
	if err != nil || len(ips) == 0 {
		return net.DialTimeout("tcp", net.JoinHostPort(host, port), timeout)
	}
	perAddr := timeout / time.Duration(len(ips))
	if perAddr > 10*time.Second {
		perAddr = 10 * time.Second
	}
	if perAddr < 5*time.Second {
		perAddr = 5 * time.Second
	}
	var lastErr error
	for _, ip := range ips {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, port), perAddr)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("all %d address(es) unreachable for %s: %w", len(ips), host, lastErr)
}

func smtpSend(conn net.Conn, host, username, password, from, to string, msg []byte) error {
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("SMTP client: %w", err)
	}
	defer c.Quit()
	if username != "" {
		if err := c.Auth(smtp.PlainAuth("", username, password, host)); err != nil {
			return fmt.Errorf("SMTP auth: %w", err)
		}
	}
	return smtpDeliver(c, from, to, msg)
}

func stripCRLF(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\r' && s[i] != '\n' {
			out = append(out, s[i])
		}
	}
	return string(out)
}

func smtpDeliver(c *smtp.Client, from, to string, msg []byte) error {
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("SMTP MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("SMTP RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("SMTP write: %w", err)
	}
	return w.Close()
}
