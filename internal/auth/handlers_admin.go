package auth

import (
	"encoding/csv"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// GET /api/admin/users?search=&role=&per_page=&page=  (Laravel paginator shape)
func (h *Handler) AdminListUsers(c *gin.Context) {
	q := h.svc.db.Model(&User{})
	if s := strings.TrimSpace(c.Query("search")); s != "" {
		like := "%" + strings.ToLower(s) + "%"
		q = q.Where("LOWER(name) LIKE ? OR LOWER(email) LIKE ?", like, like)
	}
	if r := c.Query("role"); r != "" {
		q = q.Where("role = ?", r)
	}
	perPage := clamp(atoiDefault(c.Query("per_page"), 15), 1, 100)
	page := max(atoiDefault(c.Query("page"), 1), 1)

	var total int64
	q.Count(&total)
	users := []User{}
	q.Order("id").Offset((page - 1) * perPage).Limit(perPage).Find(&users)

	last := max(int(math.Ceil(float64(total)/float64(perPage))), 1)
	path := h.cfg.PublicURL + "/api/admin/users"
	pageURL := func(p int) any {
		if p < 1 || p > last {
			return nil
		}
		return fmt.Sprintf("%s?page=%d", path, p)
	}
	var from, to any
	if len(users) > 0 {
		from, to = (page-1)*perPage+1, (page-1)*perPage+len(users)
	}
	ok(c, http.StatusOK, gin.H{
		"current_page": page, "data": users, "per_page": perPage, "total": total,
		"last_page": last, "from": from, "to": to, "path": path,
		"first_page_url": pageURL(1), "last_page_url": pageURL(last),
		"next_page_url": pageURL(page + 1), "prev_page_url": pageURL(page - 1),
	})
}

// GET /api/admin/users/:id
func (h *Handler) AdminShowUser(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	u, err := h.svc.UserByID(id)
	if err != nil {
		fail(c, http.StatusNotFound, "User not found")
		return
	}
	history := []RoleChangeLog{}
	h.svc.db.Preload("Changer").Where("user_id = ?", u.ID).Order("created_at DESC").Find(&history)
	ok(c, http.StatusOK, gin.H{"user": u, "role_change_history": history})
}

// PUT /api/admin/users/:id/role {role}
func (h *Handler) AdminUpdateRole(c *gin.Context) {
	var in struct {
		Role string `json:"role"`
	}
	_ = c.ShouldBindJSON(&in)
	if !validRoles[in.Role] {
		validationError(c, "role", "The selected role is invalid.")
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	u, err := h.svc.UserByID(id)
	if err != nil {
		fail(c, http.StatusNotFound, "User not found")
		return
	}
	admin := CurrentUser(c)
	if admin.ID == u.ID {
		fail(c, http.StatusForbidden, "You cannot change your own role.")
		return
	}
	old := u.Role
	h.svc.db.Model(u).Update("role", in.Role)
	h.svc.db.Create(&RoleChangeLog{UserID: u.ID, ChangedBy: admin.ID, OldRole: old, NewRole: in.Role})
	// The user's open sessions pick up the new role on their next request
	u, _ = h.svc.UserByID(u.ID)
	ok(c, http.StatusOK, u, "User role updated successfully.")
}

func (h *Handler) countUsers(where string, args ...any) int64 {
	var n int64
	q := h.svc.db.Model(&User{})
	if where != "" {
		q = q.Where(where, args...)
	}
	q.Count(&n)
	return n
}

func (h *Handler) activeUsersSince(t time.Time) int64 {
	var n int64
	h.svc.db.Model(&Session{}).Where("last_activity >= ?", t).Distinct("user_id").Count(&n)
	return n
}

func startOfWeek(t time.Time) time.Time {
	d := (int(t.Weekday()) + 6) % 7 // Monday = 0, as Carbon
	y, m, day := t.AddDate(0, 0, -d).Date()
	return time.Date(y, m, day, 0, 0, 0, 0, t.Location())
}

func startOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}

// GET /api/admin/stats
func (h *Handler) AdminStats(c *gin.Context) {
	now := time.Now()
	ok(c, http.StatusOK, gin.H{
		"total_users":          h.countUsers(""),
		"students":             h.countUsers("role = ?", RoleStudent),
		"teachers":             h.countUsers("role = ?", RoleTeacher),
		"admins":               h.countUsers("role = ?", RoleAdmin),
		"new_users_this_week":  h.countUsers("created_at >= ?", startOfWeek(now)),
		"new_users_this_month": h.countUsers("created_at >= ?", startOfMonth(now)),
	})
}

// GET /api/admin/analytics/overview
func (h *Handler) AnalyticsOverview(c *gin.Context) {
	now := time.Now()
	cur := startOfMonth(now)
	prev := startOfMonth(cur.AddDate(0, -1, 0))
	thisMonth := h.countUsers("created_at >= ?", cur)
	lastMonth := h.countUsers("created_at >= ? AND created_at < ?", prev, cur)
	growth := 0.0
	switch {
	case lastMonth > 0:
		growth = math.Round(float64(thisMonth-lastMonth)/float64(lastMonth)*10000) / 100
	case thisMonth > 0:
		growth = 100
	}
	ok(c, http.StatusOK, gin.H{
		"total_users":          h.countUsers(""),
		"active_users":         h.activeUsersSince(now.AddDate(0, 0, -30)),
		"new_users_this_week":  h.countUsers("created_at >= ?", startOfWeek(now)),
		"new_users_this_month": thisMonth,
		"role_distribution": gin.H{
			"students": h.countUsers("role = ?", RoleStudent),
			"teachers": h.countUsers("role = ?", RoleTeacher),
			"admins":   h.countUsers("role = ?", RoleAdmin),
		},
		"growth_percentage": growth,
	})
}

// GET /api/admin/analytics/users/growth?period=daily|weekly|monthly&from=&to=
func (h *Handler) AnalyticsUserGrowth(c *gin.Context) {
	format := map[string]string{"daily": "YYYY-MM-DD", "weekly": `IYYY-"W"IW`}[c.Query("period")]
	if format == "" {
		format = "YYYY-MM"
	}
	to, from := time.Now(), time.Now().AddDate(-1, 0, 0)
	if t, err := time.Parse("2006-01-02", c.Query("from")); err == nil {
		from = t
	}
	if t, err := time.Parse("2006-01-02", c.Query("to")); err == nil {
		to = t.Add(24*time.Hour - time.Nanosecond)
	}
	var rows []struct {
		Period string
		Count  int64
	}
	h.svc.db.Model(&User{}).
		Select("to_char(created_at, ?) AS period, COUNT(*) AS count", format).
		Where("created_at BETWEEN ? AND ?", from, to).
		Group("period").Order("period").Scan(&rows)
	cumulative := h.countUsers("created_at < ?", from)
	data := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		cumulative += r.Count
		data = append(data, gin.H{"period": r.Period, "count": r.Count, "cumulative": cumulative})
	}
	ok(c, http.StatusOK, data)
}

// GET /api/admin/analytics/users/activity?days=30
func (h *Handler) AnalyticsUserActivity(c *gin.Context) {
	days := clamp(atoiDefault(c.Query("days"), 30), 1, 365)
	now := time.Now()
	y, m, d := now.Date()
	recent := h.activeUsersSince(now.AddDate(0, 0, -days))
	ok(c, http.StatusOK, gin.H{
		"dau":             h.activeUsersSince(time.Date(y, m, d, 0, 0, 0, 0, now.Location())),
		"mau":             h.activeUsersSince(startOfMonth(now)),
		"recently_active": recent,
		"inactive_count":  max(h.countUsers("")-recent, 0),
	})
}

// GET /api/admin/analytics/users/roles
func (h *Handler) AnalyticsRoles(c *gin.Context) {
	total := h.countUsers("")
	roles := make([]gin.H, 0, 3)
	for _, r := range []string{RoleStudent, RoleTeacher, RoleAdmin} {
		n := h.countUsers("role = ?", r)
		pct := 0.0
		if total > 0 {
			pct = math.Round(float64(n)/float64(total)*10000) / 100
		}
		roles = append(roles, gin.H{"role": r, "count": n, "percentage": pct})
	}
	recent := []RoleChangeLog{}
	h.svc.db.Preload("User").Preload("Changer").Order("created_at DESC").Limit(20).Find(&recent)
	ok(c, http.StatusOK, gin.H{"roles": roles, "recent_changes": recent})
}

// GET /api/admin/analytics/reports/export?type=users|activity&format=json|csv
func (h *Handler) AnalyticsExport(c *gin.Context) {
	typ := c.DefaultQuery("type", "users")
	type userRow struct {
		ID        int       `json:"id"`
		Name      string    `json:"name"`
		Email     string    `json:"email"`
		Role      string    `json:"role"`
		CreatedAt time.Time `json:"created_at"`
	}
	type activityRow struct {
		ID           int       `json:"id"`
		Name         string    `json:"name"`
		Email        string    `json:"email"`
		Role         string    `json:"role"`
		LastActivity time.Time `json:"last_activity"`
		IPAddress    string    `json:"ip_address"`
	}
	var header []string
	var records [][]string
	var data any
	if typ == "users" {
		rows := []userRow{}
		h.svc.db.Model(&User{}).Select("id, name, email, role, created_at").Order("id").Scan(&rows)
		header = []string{"ID", "Name", "Email", "Role", "Created At"}
		for _, r := range rows {
			records = append(records, []string{strconv.Itoa(r.ID), r.Name, r.Email, r.Role, r.CreatedAt.Format(time.DateTime)})
		}
		data = rows
	} else {
		rows := []activityRow{}
		h.svc.db.Table("sessions").Joins("JOIN users ON users.id = sessions.user_id").
			Select("users.id, users.name, users.email, users.role, sessions.last_activity, sessions.ip_address").
			Where("sessions.last_activity >= ?", time.Now().AddDate(0, 0, -30)).
			Order("sessions.last_activity DESC").Scan(&rows)
		header = []string{"ID", "Name", "Email", "Role", "Last Activity", "IP Address"}
		for _, r := range rows {
			records = append(records, []string{strconv.Itoa(r.ID), r.Name, r.Email, r.Role, r.LastActivity.Format(time.DateTime), r.IPAddress})
		}
		data = rows
	}
	if c.Query("format") != "csv" {
		ok(c, http.StatusOK, data)
		return
	}
	name := fmt.Sprintf("%s_report_%s.csv", typ, time.Now().Format("2006-01-02_150405"))
	c.Header("Content-Type", "text/csv")
	c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
	w := csv.NewWriter(c.Writer)
	_ = w.Write(header)
	for _, r := range records {
		_ = w.Write(csvSafe(r))
	}
	w.Flush()
}

// csvSafe stops spreadsheet formula injection (=, +, -, @ at cell start).
func csvSafe(r []string) []string {
	out := make([]string, len(r))
	for i, v := range r {
		if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
			v = "'" + v
		}
		out[i] = v
	}
	return out
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func clamp(v, lo, hi int) int { return min(max(v, lo), hi) }
