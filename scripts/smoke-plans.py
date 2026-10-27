#!/usr/bin/env python3
"""Plans (FEATURES.md F4.2): free limits, Pro set by an admin, institution
members get Pro limits. Needs an API started with ADMIN_EMAIL/ADMIN_PASSWORD
(passed as env here too) and FREE_COURSE_LIMIT=2, NEW_COURSES_PER_DAY=0:
    BASE=http://localhost:18095 ADMIN_EMAIL=a@x.io ADMIN_PASSWORD=... python3 scripts/smoke-plans.py"""
import os, sys, time, importlib.util
spec = importlib.util.spec_from_file_location("q", os.path.join(os.path.dirname(__file__), "smoke-quiz.py"))
src = open(spec.origin).read()
ns = {}
exec(src[:src.index("run = str(int(time.time()))")], ns)
User, t, check = ns["User"], ns["t"], ns["check"]

run = str(int(time.time()))
admin, fay, gus = User("admin"), User("Fay Free"), User("Gus Group")
s, b = admin.req("POST", "/login", {"email": os.environ["ADMIN_EMAIL"], "password": os.environ["ADMIN_PASSWORD"]})
t("admin signs in", 200, s, b)
ids = {}
for u in (fay, gus):
    s, b = u.req("POST", "/register", {"name": u.name, "email": f"{u.name.split()[0].lower()}{run}@p.test", "password": "secret123", "password_confirmation": "secret123"})
    t(f"register {u.name}", 201, s, b)
    ids[u] = u.req("GET", "/api/learn/me")[1]["data"]

me = fay.req("GET", "/api/learn/me")[1]["data"]
check("Fay starts on the free plan (2 courses)", me["plan"] == "free" and me["limits"]["courses_per_user"] == 2, me.get("plan"))
for i in range(2):
    t(f"course {i+1}", 201, fay.req("POST", "/api/course", form={"course_title": f"C{i}", "course_description": "x", "course_category_id": "1"})[0])
s, b = fay.req("POST", "/api/course", form={"course_title": "C3", "course_description": "x", "course_category_id": "1"})
t("a third course is blocked on free", 403, s, b)
uid = admin.req("GET", "/api/admin/users?search=" + f"fay{run}")[1]
fay_id = None
for k in ("data", "users"):
    rows = uid.get(k) if isinstance(uid, dict) else None
    if isinstance(rows, dict):
        rows = rows.get("data") or rows.get("users")
    if rows:
        fay_id = next((r["id"] for r in rows if r["email"].startswith(f"fay{run}")), None)
check("admin finds Fay", fay_id is not None, uid)
t("Fay cannot set her own plan", 403, fay.req("PUT", f"/api/admin/users/{fay_id}/plan", {"plan": "pro"})[0])
s, b = admin.req("PUT", f"/api/admin/users/{fay_id}/plan", {"plan": "gold"})
t("unknown plan refused", 422, s, b)
s, b = admin.req("PUT", f"/api/admin/users/{fay_id}/plan", {"plan": "pro", "until": "2099-01-01T00:00:00Z"})
t("admin moves Fay to Pro", 200, s, b)
me = fay.req("GET", "/api/learn/me")[1]["data"]
check("Pro limits apply", me["plan"] == "pro" and me["limits"]["courses_per_user"] == 50 and me["can_create_course"], me.get("limits"))
t("now the third course works", 201, fay.req("POST", "/api/course", form={"course_title": "C3", "course_description": "x", "course_category_id": "1"})[0])
admin.req("PUT", f"/api/admin/users/{fay_id}/plan", {"plan": "pro", "until": "2001-01-01T00:00:00Z"})
check("an ended Pro plan is free again", fay.req("GET", "/api/learn/me")[1]["data"]["plan"] == "free")
s, b = gus.req("POST", "/api/learn/institutions", {"name": "Gus School"})
t("Gus creates an institution", 201, s, b)
me = gus.req("GET", "/api/learn/me")[1]["data"]
check("institution members get Pro limits", me["plan"] == "institution" and me["limits"]["courses_per_user"] == 50, me.get("plan"))

print(f"\npassed {ns['passed']}, failed {ns['failed']}")
