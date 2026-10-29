package auth

import (
	"fmt"
	"html"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ── contact form on the home page ───────────────────────────────────────
//
// POST /contact emails the message to CONTACT_EMAIL (or ADMIN_EMAIL). The
// browser reaches it through the Nuxt proxy, so every request shares the
// proxy's IP: limits are per sender email and for the whole site instead.

type contactInput struct {
	Name    string `json:"name" form:"name"`
	Email   string `json:"email" form:"email"`
	Company string `json:"company" form:"company"`
	Phone   string `json:"phone" form:"phone"`
	Country string `json:"country" form:"country"`
	Message string `json:"message" form:"message"`
	Website string `json:"website" form:"website"` // hidden field: only bots fill it
}

const (
	contactPerSender = 3  // messages per sender email per window
	contactPerSite   = 30 // messages for the whole site per window
	contactWindow    = time.Hour
)

type contactLimit struct {
	mu   sync.Mutex
	sent []contactSend
}

type contactSend struct {
	email string
	at    time.Time
}

var contactLimiter = &contactLimit{}

// allow records a send and reports whether it is within both limits.
func (l *contactLimit) allow(email string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := now.Add(-contactWindow)
	kept := l.sent[:0]
	mine := 0
	for _, s := range l.sent {
		if s.at.After(cut) {
			kept = append(kept, s)
			if s.email == email {
				mine++
			}
		}
	}
	l.sent = kept
	if mine >= contactPerSender || len(l.sent) >= contactPerSite {
		return false
	}
	l.sent = append(l.sent, contactSend{email: email, at: now})
	return true
}

func (h *Handler) Contact(c *gin.Context) {
	var in contactInput
	_ = c.ShouldBind(&in)
	in.Name = strings.TrimSpace(in.Name)
	in.Email = normaliseEmail(in.Email)
	in.Message = strings.TrimSpace(in.Message)

	// Bots that fill the hidden field get a normal answer and no email.
	if in.Website != "" {
		c.JSON(http.StatusOK, gin.H{"message": "Thanks, your message has been sent."})
		return
	}
	switch {
	case in.Name == "" || len(in.Name) > 120:
		validationError(c, "name", "Please enter your name.")
		return
	case !validEmail(in.Email):
		validationError(c, "email", "Please enter a valid email address.")
		return
	case len(in.Message) < 5 || len(in.Message) > 5000:
		validationError(c, "message", "Please write a message (up to 5000 characters).")
		return
	case len(in.Company) > 120 || len(in.Phone) > 40 || len(in.Country) > 60:
		validationError(c, "message", "Some fields are too long.")
		return
	}

	to := h.cfg.ContactEmail
	if to == "" {
		to = h.cfg.AdminEmail
	}
	if to == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "The contact form is not set up yet. Please try again later."})
		return
	}
	if !contactLimiter.allow(in.Email, time.Now()) {
		c.JSON(http.StatusTooManyRequests, gin.H{"message": "Too many messages. Please try again in an hour."})
		return
	}

	row := func(label, v string) string {
		if strings.TrimSpace(v) == "" {
			return ""
		}
		return fmt.Sprintf("<tr><td style=\"padding:4px 12px 4px 0;color:#6b7280\">%s</td><td>%s</td></tr>", label, html.EscapeString(v))
	}
	body := fmt.Sprintf(`<html><body style="font-family:Arial,sans-serif;color:#1f2937">
<h2>New message from the FocusFlow website</h2>
<table>%s%s%s%s%s</table>
<p style="white-space:pre-wrap">%s</p>
<p style="font-size:13px;color:#6b7280">Reply to %s to answer.</p>
</body></html>`,
		row("Name", in.Name), row("Email", in.Email), row("Organisation", in.Company),
		row("Phone", in.Phone), row("Country", in.Country),
		html.EscapeString(in.Message), html.EscapeString(in.Email))
	h.sendMailAsync(to, "FocusFlow contact: "+in.Name, body)

	c.JSON(http.StatusOK, gin.H{"message": "Thanks, your message has been sent. We'll reply by email."})
}
