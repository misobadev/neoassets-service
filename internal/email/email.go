package email

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"time"

	mail "github.com/go-mail/mail/v2"
	"github.com/rs/zerolog/log"
)

// DefaultLogoURL is the NeoAssets brand mark used in email headers.
const DefaultLogoURL = "https://neoassets.dev/neoassets-isotype.svg"

// Config holds the SMTP settings for sending account emails.
type Config struct {
	Host        string
	Port        int
	Username    string
	Password    string
	FromEmail   string
	FromName    string
	FrontendURL string
	LogoURL     string
}

// Sender sends transactional account emails over SMTP.
type Sender struct {
	dialer      *mail.Dialer
	fromEmail   string
	fromName    string
	frontendURL string
	logoURL     string
}

// NewSender validates the SMTP config and builds a dialer.
func NewSender(cfg Config) (*Sender, error) {
	if cfg.Host == "" || cfg.Username == "" || cfg.Password == "" || cfg.FromEmail == "" {
		return nil, fmt.Errorf("SMTP_HOST, SMTP_USERNAME, SMTP_PASSWORD and SMTP_FROM_EMAIL are required")
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	if cfg.FromName == "" {
		cfg.FromName = "NeoAssets Team"
	}
	if cfg.LogoURL == "" {
		cfg.LogoURL = DefaultLogoURL
	}
	if cfg.FrontendURL == "" {
		cfg.FrontendURL = "http://localhost:8091"
	}

	d := mail.NewDialer(cfg.Host, cfg.Port, cfg.Username, cfg.Password)
	d.Timeout = 15 * time.Second

	return &Sender{
		dialer:      d,
		fromEmail:   cfg.FromEmail,
		fromName:    cfg.FromName,
		frontendURL: strings.TrimRight(cfg.FrontendURL, "/"),
		logoURL:     cfg.LogoURL,
	}, nil
}

// send delivers a text/html email and logs the outcome.
func (s *Sender) send(to, subject, textBody, htmlBody string) error {
	m := mail.NewMessage()
	m.SetHeader("From", s.dialerUsername(s.fromName, s.fromEmail))
	m.SetHeader("To", to)
	m.SetHeader("Subject", subject)
	m.SetBody("text/plain", textBody)
	m.AddAlternative("text/html", htmlBody)

	if err := s.dialer.DialAndSend(m); err != nil {
		log.Error().Str("to", to).Err(err).Msg("email send failed")
		return err
	}
	log.Info().Str("to", to).Str("subject", subject).Msg("email sent")
	return nil
}

func (s *Sender) dialerUsername(name, email string) string {
	if name == "" {
		return email
	}
	return fmt.Sprintf("%s <%s>", name, email)
}

// SendVerificationEmail emails a link that verifies the account email.
func (s *Sender) SendVerificationEmail(to, username, token string) error {
	link := fmt.Sprintf("%s/app/login?verify=%s", s.frontendURL, token)
	subject := "Verify your NeoAssets email"
	htmlBody := renderTemplate(verificationHTML, map[string]string{
		"LogoURL":    s.logoURL,
		"Username":   username,
		"VerifyLink": link,
	})
	textBody := fmt.Sprintf("Hi %s,\n\nVerify your email to start submitting system art packs:\n%s\n\nIf you did not create an account, you can ignore this email.", username, link)
	return s.send(to, subject, textBody, htmlBody)
}

// SendPasswordResetEmail emails a link that lets the user set a new password.
func (s *Sender) SendPasswordResetEmail(to, username, token string) error {
	link := fmt.Sprintf("%s/app/login?reset=%s", s.frontendURL, token)
	subject := "Reset your NeoAssets password"
	htmlBody := renderTemplate(resetHTML, map[string]string{
		"LogoURL":   s.logoURL,
		"Username":  username,
		"ResetLink": link,
	})
	textBody := fmt.Sprintf("Hi %s,\n\nWe received a request to reset your password. Set a new one here (the link expires in 1 hour):\n%s\n\nIf you did not request this, you can ignore this email.", username, link)
	return s.send(to, subject, textBody, htmlBody)
}

// SendDonorClaimEmail emails a one-time code that proves the user owns the
// email a donation was made with.
func (s *Sender) SendDonorClaimEmail(to, username, code string) error {
	subject := "Confirm your NeoAssets donation email"
	htmlBody := renderTemplate(donorClaimHTML, map[string]string{
		"LogoURL":  s.logoURL,
		"Username": username,
		"Code":     code,
	})
	textBody := fmt.Sprintf("Hi %s,\n\nUse this code to claim the donation made with this email: %s\n\nThe code expires in 15 minutes. If you did not request this, you can ignore this email.", username, code)
	return s.send(to, subject, textBody, htmlBody)
}

func renderTemplate(tpl string, data map[string]string) string {
	t, err := template.New("email").Parse(tpl)
	if err != nil {
		return tpl
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return tpl
	}
	return buf.String()
}

const verificationHTML = `<!DOCTYPE html>
<html lang="en"><body style="margin:0;padding:0;background:#0a0a0c;font-family:Inter,system-ui,sans-serif;">
<div style="max-width:600px;margin:0 auto;padding:32px 16px;">
  <div style="text-align:center;padding:24px 0;">
    <img src="{{.LogoURL}}" alt="NeoAssets" style="height:44px;width:auto;" />
  </div>
  <div style="background:#111114;border:1px solid #1d1d24;border-radius:16px;padding:32px;color:#f4f4f5;">
    <h1 style="font-size:20px;margin:0 0 16px;">Verify your email</h1>
    <p style="color:#a1a1aa;font-size:14px;line-height:1.6;margin:0 0 24px;">
      Hi {{.Username}},<br/>Click the button below to verify your email and start submitting system art packs.
    </p>
    <a href="{{.VerifyLink}}" style="display:inline-block;background:#7c6cff;color:#ffffff;text-decoration:none;font-weight:600;padding:12px 24px;border-radius:10px;">Verify email</a>
    <p style="color:#71717a;font-size:12px;line-height:1.6;margin:24px 0 0;">
      If you did not create an account, you can safely ignore this email.<br/>
      <a href="{{.VerifyLink}}" style="color:#7c6cff;">{{.VerifyLink}}</a>
    </p>
  </div>
  <p style="text-align:center;color:#52525b;font-size:12px;margin:24px 0;">&copy; 2026 NeoAssets</p>
</div>
</body></html>`

const resetHTML = `<!DOCTYPE html>
<html lang="en"><body style="margin:0;padding:0;background:#0a0a0c;font-family:Inter,system-ui,sans-serif;">
<div style="max-width:600px;margin:0 auto;padding:32px 16px;">
  <div style="text-align:center;padding:24px 0;">
    <img src="{{.LogoURL}}" alt="NeoAssets" style="height:44px;width:auto;" />
  </div>
  <div style="background:#111114;border:1px solid #1d1d24;border-radius:16px;padding:32px;color:#f4f4f5;">
    <h1 style="font-size:20px;margin:0 0 16px;">Reset your password</h1>
    <p style="color:#a1a1aa;font-size:14px;line-height:1.6;margin:0 0 24px;">
      Hi {{.Username}},<br/>We received a request to reset your NeoAssets password. The link below expires in 1 hour.
    </p>
    <a href="{{.ResetLink}}" style="display:inline-block;background:#7c6cff;color:#ffffff;text-decoration:none;font-weight:600;padding:12px 24px;border-radius:10px;">Reset password</a>
    <p style="color:#71717a;font-size:12px;line-height:1.6;margin:24px 0 0;">
      If you did not request this, you can safely ignore this email.<br/>
      <a href="{{.ResetLink}}" style="color:#7c6cff;">{{.ResetLink}}</a>
    </p>
  </div>
  <p style="text-align:center;color:#52525b;font-size:12px;margin:24px 0;">&copy; 2026 NeoAssets</p>
</div>
</body></html>`

const donorClaimHTML = `<!DOCTYPE html>
<html lang="en"><body style="margin:0;padding:0;background:#0a0a0c;font-family:Inter,system-ui,sans-serif;">
<div style="max-width:600px;margin:0 auto;padding:32px 16px;">
  <div style="text-align:center;padding:24px 0;">
    <img src="{{.LogoURL}}" alt="NeoAssets" style="height:44px;width:auto;" />
  </div>
  <div style="background:#111114;border:1px solid #1d1d24;border-radius:16px;padding:32px;color:#f4f4f5;">
    <h1 style="font-size:20px;margin:0 0 16px;">Confirm your donation email</h1>
    <p style="color:#a1a1aa;font-size:14px;line-height:1.6;margin:0 0 24px;">
      Hi {{.Username}},<br/>Use the code below to link the donations made with this email to your NeoAssets account.
    </p>
    <div style="display:inline-block;background:#7c6cff;color:#ffffff;font-weight:700;letter-spacing:6px;font-size:24px;padding:12px 24px;border-radius:10px;">{{.Code}}</div>
    <p style="color:#71717a;font-size:12px;line-height:1.6;margin:24px 0 0;">
      The code expires in 15 minutes. If you did not request this, you can safely ignore this email.
    </p>
  </div>
  <p style="text-align:center;color:#52525b;font-size:12px;margin:24px 0;">&copy; 2026 NeoAssets</p>
</div>
</body></html>`
