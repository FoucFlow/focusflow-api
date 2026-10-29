package auth

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func testService() *Service {
	return NewService(nil, "test-app-key-0123456789abcdef0123456789", 30*24*time.Hour)
}

func TestCheckPasswordAcceptsLaravelHashes(t *testing.T) {
	h, err := bcrypt.GenerateFromPassword([]byte("secret123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	// PHP writes the same bcrypt with a $2y$ prefix
	laravel := "$2y$" + strings.TrimPrefix(string(h), "$2a$")
	if !CheckPassword(&laravel, "secret123") {
		t.Fatal("a Laravel $2y$ hash must verify")
	}
	if CheckPassword(&laravel, "wrong") {
		t.Fatal("wrong password must not verify")
	}
}

func TestCheckPasswordWithoutPassword(t *testing.T) {
	if CheckPassword(nil, "") || CheckPassword(new(string), "") {
		t.Fatal("accounts without a password (Google-only) must never match")
	}
}

func TestOAuthState(t *testing.T) {
	s := testService()
	st := s.NewOAuthState()
	if !s.CheckOAuthState(st) {
		t.Fatal("fresh state must be valid")
	}
	p := strings.Split(st, ".")
	if s.CheckOAuthState(p[0] + "x." + p[1] + "." + p[2]) {
		t.Fatal("tampered nonce must fail")
	}
	expired := strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)
	if s.CheckOAuthState(p[0] + "." + expired + "." + s.sign("oauth", p[0], expired)) {
		t.Fatal("expired state must fail")
	}
	other := NewService(nil, "another-key-0123456789abcdef0123456789", time.Hour)
	if other.CheckOAuthState(st) {
		t.Fatal("state signed with another key must fail")
	}
}

func TestVerificationLinkSignature(t *testing.T) {
	s := testService()
	u := &User{ID: 7, Email: "a@b.io"}
	path := s.VerificationPath(u)
	// /verify-email/7/<hash>?expires=..&signature=..
	rest := strings.TrimPrefix(path, "/verify-email/")
	idHash, query, _ := strings.Cut(rest, "?")
	id, hash, _ := strings.Cut(idHash, "/")
	var expires, sig string
	for _, kv := range strings.Split(query, "&") {
		k, v, _ := strings.Cut(kv, "=")
		if k == "expires" {
			expires = v
		} else if k == "signature" {
			sig = v
		}
	}
	if id != "7" || hash != emailHash("a@b.io") {
		t.Fatalf("unexpected path %s", path)
	}
	if sig != s.sign("verify", id, hash, expires) {
		t.Fatal("signature mismatch")
	}
	if sig == s.sign("verify", "8", hash, expires) {
		t.Fatal("signature must bind the user id")
	}
}

func TestCSVSafe(t *testing.T) {
	got := csvSafe([]string{"=HYPERLINK(1)", "+1", "-2", "@x", "plain", ""})
	want := []string{"'=HYPERLINK(1)", "'+1", "'-2", "'@x", "plain", ""}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("csvSafe[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLoginLimiter(t *testing.T) {
	l := &limiter{hits: map[string][]time.Time{}}
	for i := 0; i < maxLoginAttempts; i++ {
		if l.retryAfter("k") > 0 {
			t.Fatalf("blocked too early at attempt %d", i)
		}
		l.hit("k")
	}
	if l.retryAfter("k") <= 0 {
		t.Fatal("must block after the limit")
	}
	l.clear("k")
	if l.retryAfter("k") > 0 {
		t.Fatal("clear must reset")
	}
}

func TestContactLimit(t *testing.T) {
	l := &contactLimit{}
	now := time.Now()
	for i := 0; i < contactPerSender; i++ {
		if !l.allow("a@test.io", now) {
			t.Fatalf("send %d should be allowed", i+1)
		}
	}
	if l.allow("a@test.io", now) {
		t.Fatal("sender over the limit should be refused")
	}
	if !l.allow("b@test.io", now) {
		t.Fatal("another sender should still be allowed")
	}
	if !l.allow("a@test.io", now.Add(contactWindow+time.Second)) {
		t.Fatal("sender should be allowed again after the window")
	}
	for i := 0; len(l.sent) < contactPerSite; i++ {
		l.allow(fmt.Sprintf("s%d@test.io", i), now.Add(contactWindow+time.Second))
	}
	if l.allow("new@test.io", now.Add(contactWindow+time.Second)) {
		t.Fatal("site over the limit should be refused")
	}
}
