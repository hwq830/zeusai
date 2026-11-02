package auth

import (
	"context"
	"crypto/tls"
	"fmt"
	"github.com/hwq830/zeusai/server/internal/i18n"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// SMTPEmail requires TLS, including certificate validation, before sending credentials
// or a one-time code. Port 465 uses implicit TLS; other ports require STARTTLS.
type SMTPEmail struct {
	Host                     string
	Port                     int
	Username, Password, From string
}

func (s SMTPEmail) SendCode(ctx context.Context, to, code string) error {
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return err
	}
	if _, err = NormalizeEmail(to); err != nil {
		return err
	}
	if strings.ContainsAny(s.From, "\r\n") || len(code) != 6 || strings.Trim(code, "0123456789") != "" {
		return fmt.Errorf("invalid mail data")
	}
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	tlsConfig := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	var conn net.Conn
	if s.Port == 465 {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return err
	}
	defer c.Close()
	if s.Port != 465 {
		if err = c.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if s.Username != "" {
		if err = c.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return err
		}
	}
	if err = c.Mail(from.Address); err != nil {
		return err
	}
	if err = c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	text := i18n.FromContext(ctx).Pick("你的 Zeus 登录验证码是 "+code+"，5 分钟内有效。", "Your Zeus sign-in code is "+code+". It expires in 5 minutes.")
	_, err = fmt.Fprintf(w, "From: %s\r\nTo: %s\r\nSubject: Your Zeus sign-in code\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", s.From, to, text)
	if err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	_ = c.Quit()
	return nil
}
