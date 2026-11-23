// Package accountmail sends the account emails of roadmap 113 P2 — invitations and password-reset
// links — over SMTP. It owns the wording and the links; pkg/identity owns the tokens.
//
// Sent synchronously from the request that triggers it, with a short timeout: an invitation the
// relay refused is still created, and the inviting admin gets the link back to pass on by hand.
// Production points SMTP_* at a real relay; dev points it at waap's devtools/mail-mock.
package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// SendTimeout bounds one email (connect + conversation).
const SendTimeout = 10 * time.Second

// Config is the relay and the portal the links point at.
type Config struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
	// PublicURL is the portal's origin, e.g. https://portal.example.com (links are PublicURL +
	// /accept-invite?token=… and /reset-password?token=…).
	PublicURL string
}

// ErrNotConfigured: no SMTP_HOST/SMTP_FROM, so nothing can be sent.
var ErrNotConfigured = errors.New("email is not configured (SMTP_HOST, SMTP_FROM)")

// SendFunc delivers one message.
type SendFunc func(ctx context.Context, cfg Config, to string, msg []byte) error

// Mailer sends account emails. The zero value (or nil) sends nothing and reports ErrNotConfigured.
type Mailer struct {
	cfg  Config
	send SendFunc
}

func New(cfg Config) *Mailer {
	return &Mailer{cfg: cfg, send: sendSMTP}
}

// WithSender replaces SMTP delivery (a test capturing what would have been sent).
func (m *Mailer) WithSender(fn SendFunc) *Mailer {
	m.send = fn
	return m
}

// Configured reports whether emails can be sent at all.
func (m *Mailer) Configured() bool {
	return m != nil && m.cfg.Host != "" && m.cfg.From != ""
}

// Link is PublicURL + path?token=token ("" path segments when PublicURL is unset: a relative link
// the portal still resolves).
func (m *Mailer) Link(path, token string) string {
	base := ""
	if m != nil {
		base = strings.TrimRight(m.cfg.PublicURL, "/")
	}
	return base + path + "?token=" + url.QueryEscape(token)
}

// Invitation is what an invitation email says.
type Invitation struct {
	To          string
	TenantName  string
	InviterName string
	Link        string
	ExpiresAt   time.Time
	// NewAccount: the person sets a password on accepting (else they confirm their existing one).
	NewAccount bool
}

func (m *Mailer) SendInvitation(ctx context.Context, inv Invitation) error {
	tenant := inv.TenantName
	if tenant == "" {
		tenant = "the portal"
	}
	inviter := inv.InviterName
	if inviter == "" {
		inviter = "An administrator"
	}
	next := "Bạn sẽ đặt mật khẩu cho tài khoản mới. / You'll set a password for your new account."
	if !inv.NewAccount {
		next = "Bạn đã có tài khoản: xác nhận bằng mật khẩu hiện tại. / You already have an account: confirm with your current password."
	}
	body := fmt.Sprintf(`%[1]s mời bạn tham gia %[2]s.
%[1]s invited you to join %[2]s.

Nhận lời mời / Accept the invitation:
%[3]s

%[4]s

Link hết hạn lúc / The link expires at %[5]s.
Nếu bạn không chờ thư này, cứ bỏ qua. / If you weren't expecting this, ignore it.
`, inviter, tenant, inv.Link, next, inv.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC"))
	return m.deliver(ctx, inv.To, "Lời mời tham gia "+tenant+" / Invitation to "+tenant, body)
}

// PasswordReset is what a reset email says.
type PasswordReset struct {
	To        string
	Link      string
	ExpiresAt time.Time
}

func (m *Mailer) SendPasswordReset(ctx context.Context, r PasswordReset) error {
	body := fmt.Sprintf(`Có yêu cầu đặt lại mật khẩu cho tài khoản này.
Someone asked to reset the password of this account.

Đặt mật khẩu mới / Set a new password:
%s

Link dùng được một lần, hết hạn lúc / Single use, expires at %s.
Đặt xong, mọi phiên đăng nhập đang mở sẽ bị đăng xuất. / Every open session is signed out afterwards.
Nếu không phải bạn yêu cầu, cứ bỏ qua thư này: mật khẩu không đổi. / If it wasn't you, ignore this email: your password stays the same.
`, r.Link, r.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC"))
	return m.deliver(ctx, r.To, "Đặt lại mật khẩu / Reset your password", body)
}

// SignupVerification is what the signup confirmation email says.
type SignupVerification struct {
	To         string
	TenantName string
	Link       string
	ExpiresAt  time.Time
}

func (m *Mailer) SendSignupVerification(ctx context.Context, v SignupVerification) error {
	body := fmt.Sprintf(`Bạn (hoặc ai đó) đã đăng ký không gian làm việc "%s" bằng địa chỉ email này.
Someone registered the workspace "%s" with this email address.

Xác nhận và tạo không gian / Confirm and create it:
%s

Link dùng được một lần, hết hạn lúc / Single use, expires at %s.
Nếu không phải bạn, cứ bỏ qua thư này: sẽ không có gì được tạo. / If it wasn't you, ignore this email: nothing is created.
`, v.TenantName, v.TenantName, v.Link, v.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC"))
	return m.deliver(ctx, v.To, "Xác nhận đăng ký / Confirm your signup", body)
}

// OwnerRecoveryNotice tells a tenant's admins that the vendor asked to hand the tenant's
// ownership to someone else, and that they can cancel it until EffectiveAt.
type OwnerRecoveryNotice struct {
	To          string
	TenantName  string
	NewOwner    string
	RequestedBy string
	Reason      string
	EffectiveAt time.Time
	// Completed: the handover has happened (instead of being announced).
	Completed bool
}

func (m *Mailer) SendOwnerRecoveryNotice(ctx context.Context, n OwnerRecoveryNotice) error {
	tenant := n.TenantName
	if tenant == "" {
		tenant = "the workspace"
	}
	var subject, body string
	if n.Completed {
		subject = "Quyền owner đã chuyển / Ownership transferred: " + tenant
		body = fmt.Sprintf(`Quyền owner của %[1]s đã chuyển cho %[2]s (khôi phục do %[3]s yêu cầu).
Ownership of %[1]s was transferred to %[2]s (recovery requested by %[3]s).

Lý do / Reason: %[4]s
`, tenant, n.NewOwner, n.RequestedBy, n.Reason)
	} else {
		subject = "Yêu cầu khôi phục owner / Owner recovery requested: " + tenant
		body = fmt.Sprintf(`%[3]s yêu cầu chuyển quyền owner của %[1]s cho %[2]s.
%[3]s asked to hand ownership of %[1]s to %[2]s.

Lý do / Reason: %[4]s

Việc này chỉ hoàn tất sau %[5]s. Nếu bạn không đồng ý, hủy yêu cầu trong phần quản trị trước thời điểm đó.
It completes only after %[5]s. If you do not agree, cancel it in the admin area before then.
`, tenant, n.NewOwner, n.RequestedBy, n.Reason, n.EffectiveAt.UTC().Format("2006-01-02 15:04 UTC"))
	}
	return m.deliver(ctx, n.To, subject, body)
}

// WorkspaceDeletionNotice tells a workspace's admins that the owner asked to delete it, when it
// takes effect, and gives the link that cancels it (the workspace is suspended meanwhile, so the
// admin area cannot).
type WorkspaceDeletionNotice struct {
	To          string
	TenantName  string
	RequestedBy string
	EffectiveAt time.Time
	CancelLink  string
}

func (m *Mailer) SendWorkspaceDeletionNotice(ctx context.Context, n WorkspaceDeletionNotice) error {
	subject := "Yêu cầu xoá workspace / Workspace deletion requested: " + n.TenantName
	body := fmt.Sprintf(`%[2]s đã yêu cầu xoá workspace %[1]s. Workspace bị tạm khoá và sẽ bị xoá sau %[3]s.
%[2]s asked to delete the workspace %[1]s. It is suspended now and will be deleted after %[3]s.

Nếu đây là nhầm lẫn, huỷ việc xoá bằng liên kết này / If this is a mistake, cancel the deletion with this link:
%[4]s
`, n.TenantName, n.RequestedBy, n.EffectiveAt.UTC().Format("2006-01-02 15:04 UTC"), n.CancelLink)
	return m.deliver(ctx, n.To, subject, body)
}

func (m *Mailer) deliver(ctx context.Context, to, subject, body string) error {
	if !m.Configured() {
		return ErrNotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	return m.send(ctx, m.cfg, to, buildMessage(m.cfg.From, to, subject, body, time.Now()))
}

// buildMessage is a UTF-8 text/plain message, its subject an RFC 2047 encoded-word.
func buildMessage(from, to, subject, body string, now time.Time) []byte {
	var b strings.Builder
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", from)
	header("To", to)
	header("Subject", mime.BEncoding.Encode("UTF-8", subject))
	header("Date", now.Format(time.RFC1123Z))
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=UTF-8")
	header("Content-Transfer-Encoding", "8bit")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))
	return []byte(b.String())
}

// sendSMTP is net/smtp with a deadline: STARTTLS when the relay offers it, AUTH PLAIN when a user
// is configured (net/smtp refuses PLAIN over an unencrypted link to anything but localhost).
func sendSMTP(ctx context.Context, cfg Config, to string, msg []byte) error {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if cfg.User != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.User, cfg.Password, cfg.Host)); err != nil {
			return err
		}
	}
	envelope := cfg.From
	if a, err := mail.ParseAddress(cfg.From); err == nil {
		envelope = a.Address // SMTP_FROM may be "Portal <no-reply@…>"; MAIL FROM takes the address
	}
	if err := c.Mail(envelope); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
