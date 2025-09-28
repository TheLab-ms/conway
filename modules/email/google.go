package email

import (
	"bytes"
	"context"
	"fmt"
	"net/smtp"
	"time"

	"golang.org/x/oauth2/google"
	"golang.org/x/time/rate"
)

func NewGoogleSmtpSender(from string) Sender {
	creds, err := google.FindDefaultCredentialsWithParams(context.Background(), google.CredentialsParams{
		Scopes:  []string{"https://mail.google.com/"},
		Subject: from,
	})
	if err != nil {
		panic(fmt.Errorf("building google oauth token source: %w", err))
	}

	limiter := rate.NewLimiter(rate.Every(time.Second*5), 1)
	return func(ctx context.Context, to, subj string, msg []byte) error {
		err := limiter.Wait(ctx)
		if err != nil {
			return err
		}

		tok, err := creds.TokenSource.Token()
		if err != nil {
			return fmt.Errorf("getting oauth token: %w", err)
		}
		auth := &googleSmtpOauth{From: from, AccessToken: tok.AccessToken}

		buf := &bytes.Buffer{}
		fmt.Fprintf(buf, "From: TheLab Makerspace\r\n")
		fmt.Fprintf(buf, "To: %s\r\n", to)
		fmt.Fprintf(buf, "Subject: %s\r\n", subj)
		fmt.Fprintf(buf, "MIME-version: 1.0;\r\n")
		fmt.Fprintf(buf, "Content-Type: text/html; charset=\"UTF-8\";\r\n\r\n")
		buf.Write(msg)
		buf.WriteString("\r\n")

		return smtp.SendMail("smtp.gmail.com:587", auth, from, []string{to}, buf.Bytes())
	}
}

type googleSmtpOauth struct {
	From, AccessToken string
}

func (a *googleSmtpOauth) Start(_ *smtp.ServerInfo) (string, []byte, error) {
	return "XOAUTH2", []byte("user=" + a.From + "\x01" + "auth=Bearer " + a.AccessToken + "\x01\x01"), nil
}

func (a *googleSmtpOauth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return []byte(""), nil
	}
	return nil, nil
}
