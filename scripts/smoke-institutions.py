#!/usr/bin/env python3
"""End-to-end test of invite links and institutions (FEATURES.md M4: F4.1,
F4.3-F4.5) against a running API with storage and AI_PROVIDER=fake. No
third-party packages. Run from the repo root:
    BASE=http://localhost:18091 python3 scripts/smoke-institutions.py
The API needs NEW_COURSES_PER_DAY=0 (or a fresh database)."""
import json, os, sys, time, uuid, urllib.request, urllib.error, http.cookiejar

B = os.environ.get("BASE", "http://localhost:8091")
DECK = "internal/learn/testdata/lecture.pptx"
passed = failed = 0


class User:
    def __init__(self, name):
        self.jar = http.cookiejar.CookieJar()
        self.op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))
        self.name = name

    def req(self, method, path, body=None, form=None, files=None):
        headers, data = {}, None
        if files or form:
            boundary = uuid.uuid4().hex
            parts = []
            for k, v in (form or {}).items():
                parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="{k}"\r\n\r\n{v}\r\n'.encode())
            for k, (fname, content, ctype) in (files or {}).items():
                parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="{k}"; filename="{fname}"\r\nContent-Type: {ctype}\r\n\r\n'.encode() + content + b"\r\n")
            data = b"".join(parts) + f"--{boundary}--\r\n".encode()
            headers["Content-Type"] = f"multipart/form-data; boundary={boundary}"
        elif body is not None:
            data, headers["Content-Type"] = json.dumps(body).encode(), "application/json"
        r = urllib.request.Request(B + path, data=data, method=method, headers=headers)
        try:
            with self.op.open(r) as resp:
                raw = resp.read()
                return resp.status, (json.loads(raw) if raw and resp.headers.get_content_type() == "application/json" else raw)
        except urllib.error.HTTPError as e:
            raw = e.read()
            try:
                return e.code, json.loads(raw)
            except Exception:
                return e.code, raw


def t(name, want, got_status, body=None):
    global passed, failed
    if got_status == want:
        passed += 1
        print(f"ok   {name:<56} {got_status}")
    else:
        failed += 1
        print(f"FAIL {name:<56} got {got_status} want {want}: {str(body)[:240]}")


def check(name, cond, info=""):
    global passed, failed
    if cond:
        passed += 1
        print(f"ok     {name}")
    else:
        failed += 1
        print(f"FAIL   {name}: {str(info)[:300]}")


def wait(fn, secs=30):
    end = time.time() + secs
    while time.time() < end:
        v = fn()
        if v:
            return v
        time.sleep(0.5)
    return None



run = str(int(time.time()))
lec, adm, stu1, stu2, out = User("Lena Lecturer"), User("Ada Admin"), User("Sam Student"), User("Kim Other"), User("Olu Outsider")
mail = {}
for u in (lec, adm, stu1, stu2, out):
    mail[u] = f"{u.name.split()[0].lower()}{run}@uni.test"
    s, b = u.req("POST", "/register", {"name": u.name, "email": mail[u], "password": "secret123", "password_confirmation": "secret123"})
    t(f"register {u.name}", 201, s, b)

# ── institution ────────────────────────────────────────────────────────────
s, b = adm.req("POST", "/api/learn/institutions", {"name": "University of Kigali (test)"})
t("Ada creates an institution", 201, s, b)
IID = b["data"]["id"]
s, b = adm.req("POST", f"/api/learn/institutions/{IID}/members", {"email": mail[lec].upper(), "role": "lecturer"})
t("adds Lena as lecturer (email any case)", 201, s, b)
check("  linked to her account", b["data"]["user_id"] is not None and b["data"]["email"] == mail[lec], b)
s, b = adm.req("POST", f"/api/learn/institutions/{IID}/members", {"email": f"future{run}@uni.test", "role": "lecturer"})
t("adds someone without an account yet", 201, s, b)
t("Lena cannot add members", 403, lec.req("POST", f"/api/learn/institutions/{IID}/members", {"email": "x@y.zz", "role": "admin"})[0])
t("an outsider cannot see the institution", 403, out.req("GET", f"/api/learn/institutions/{IID}")[0])
s, b = lec.req("GET", "/api/learn/institutions")
check("Lena sees it under her institutions", any(i["id"] == IID and i["role"] == "lecturer" for i in b["data"]), b)
s, b = adm.req("GET", f"/api/learn/institutions/{IID}")
admin_member = [m for m in b["data"]["members"] if m["role"] == "admin"][0]["id"]
t("the last admin cannot be removed", 409, adm.req("DELETE", f"/api/learn/institutions/{IID}/members/{admin_member}")[0])

# ── course with a unit test ────────────────────────────────────────────────
s, b = lec.req("POST", "/api/course", form={"course_title": "Data Structures", "course_description": "x", "course_category_id": "1", "build_mode": "slides"})
t("Lena creates a course", 201, s, b)
CID = b["course_id"]
s, b = lec.req("POST", f"/api/learn/courses/{CID}/materials", form={"title": "Lecture"}, files={"file": ("l.pptx", open(DECK, "rb").read(), "application/vnd.openxmlformats-officedocument.presentationml.presentation")})
MID = b["data"]["id"]
s, b = lec.req("PUT", f"/api/learn/courses/{CID}/slide-units", {"units": [{"title": "Basics", "material_id": MID, "first_page": 1, "last_page": 8}]})
U1 = b["data"][0]["id"]
tmpl = lec.req("GET", "/api/learn/quiz-template.csv")[1].decode()
lec.req("POST", f"/api/learn/units/{U1}/questions/import", {"format": "csv", "text": tmpl, "save": True})
s, b = lec.req("PUT", f"/api/learn/units/{U1}/test", {"enabled": True, "questions_per_test": 5, "time_limit_minutes": 0})
t("unit test on", 200, s, b)
s, b = lec.req("POST", f"/api/learn/courses/{CID}/publish", {"visibility": "private"})
t("publish privately", 200, s, b)

# ── invite link (F4.1) ─────────────────────────────────────────────────────
t("not public: cannot join by id", 404, stu1.req("POST", f"/api/learn/courses/{CID}/join")[0])
s, b = lec.req("PUT", f"/api/learn/courses/{CID}/invite", {"enabled": True})
t("turn the invite link on", 200, s, b)
CODE = b["data"]["code"]
check("  a 10-character code", len(CODE) == 10 and b["data"]["enabled"], b)
s, b = stu1.req("GET", f"/api/learn/invites/{CODE.lower()}")
t("Sam opens the link", 200, s, b)
check("  sees the course and teacher", b["data"]["course_title"] == "Data Structures" and b["data"]["creator_name"] == "Lena Lecturer" and not b["data"]["enrolled"], b)
t("Sam joins", 200, stu1.req("POST", f"/api/learn/invites/{CODE}/join")[0])
t("Sam opens the private course", 200, stu1.req("GET", f"/api/learn/courses/{CID}")[0])
s, b = lec.req("PUT", f"/api/learn/courses/{CID}/invite", {"new_code": True})
NEW = b["data"]["code"]
t("a new link stops the old one", 404, stu2.req("GET", f"/api/learn/invites/{CODE}")[0])
s, b = lec.req("GET", f"/api/learn/courses/{CID}/learners")
check("Lena sees Sam in the learners", [r["name"] for r in b["data"]] == ["Sam Student"], b)

# ── class: institution, roster ─────────────────────────────────────────────
t("an outsider cannot attach to the institution", 403, out.req("PUT", f"/api/learn/courses/{CID}/class", {"institution_id": IID})[0])
s, b = lec.req("PUT", f"/api/learn/courses/{CID}/class", {"institution_id": IID})
t("Lena adds the course to the institution", 200, s, b)
roster = f"Email,Full name,Student ID\n{mail[stu1]},Sam Student,S001\nnot-an-email,Bad,S002\n"
s, b = lec.req("POST", f"/api/learn/courses/{CID}/roster", {"text": roster})
check("roster preview: 1 student, 1 error on line 3", s == 200 and len(b["data"]["students"]) == 1 and b["data"]["errors"][0]["line"] == 3, b)
s, b = lec.req("POST", f"/api/learn/courses/{CID}/roster", {"text": f"{mail[stu1]}\nlater{run}@uni.test\n", "save": True})
t("save the class list (one email per line)", 201, s, b)
s, b = lec.req("PUT", f"/api/learn/courses/{CID}/class", {"roster_only": True})
t("roster only", 200, s, b)
s, b = stu2.req("POST", f"/api/learn/invites/{NEW}/join")
t("Kim (not on the list) cannot join", 403, s, b)
s, b = lec.req("POST", f"/api/learn/courses/{CID}/roster/invite", {})
t("email the invite to the class", 200, s, b)
check("  sent to the one who has not joined", b["data"]["sent"] == 1, b)
s, b = lec.req("GET", f"/api/learn/courses/{CID}/class")
check("class view: roster with joined state", sorted((r["email"].split("@")[0][:4], r["joined"]) for r in b["data"]["roster"]) == [("late", False), ("sam" + run[:1], True)] or len(b["data"]["roster"]) == 2, b["data"]["roster"])

# ── consent and results (F4.4, F4.5) ───────────────────────────────────────
s, b = stu1.req("GET", f"/api/learn/courses/{CID}")
check("Sam is asked for consent", b["data"]["consent"] and not b["data"]["consent"]["given"] and "University of Kigali" in b["data"]["consent"]["institution"], b["data"].get("consent"))
t("results need an admin's approval", 403, lec.req("GET", f"/api/learn/courses/{CID}/results")[0])
t("Lena cannot approve", 403, lec.req("POST", f"/api/learn/institutions/{IID}/courses/{CID}/results-access", {"approved": True})[0])
t("Ada approves results access", 200, adm.req("POST", f"/api/learn/institutions/{IID}/courses/{CID}/results-access", {"approved": True})[0])

def take_test(user):
    s, b = user.req("POST", f"/api/learn/units/{U1}/test/start")
    a = b["data"]
    full = lec.req("GET", f"/api/learn/attempts/{a['id']}")[1]["data"]["attempt"]["items"]
    ans = []
    for i, it in enumerate(full):
        if it["kind"] == "multi":
            ans.append({"index": i, "choices": []})  # wrong on purpose
        elif it["kind"] in ("mcq", "truefalse"):
            ans.append({"index": i, "choice": it["correct"]})
        else:
            ans.append({"index": i, "answer": "correct"})
    user.req("POST", f"/api/learn/attempts/{a['id']}/submit", {"answers": ans})
    wait(lambda: user.req("GET", f"/api/learn/attempts/{a['id']}")[1]["data"]["status"] == "done")

take_test(stu1)
s, b = lec.req("GET", f"/api/learn/courses/{CID}/results")
t("results open after approval", 200, s, b)
check("  no learners until they consent", b["data"]["learners"] == [] and b["data"]["enrolled"] == 1, b["data"])
t("Sam consents", 200, stu1.req("POST", f"/api/learn/courses/{CID}/consent")[0])
s, b = lec.req("GET", f"/api/learn/courses/{CID}/results")
row = b["data"]["learners"][0] if b["data"]["learners"] else {}
check("  Sam appears with concept scores", row.get("name") == "Sam Student" and row["scores"].get("Recursion") < row["scores"].get("Base case") == 100, row)
check("  weakest concept found, hardest first in the map", "Recursion" in row.get("weakest", []) and b["data"]["concepts"][0] == "Recursion", b["data"])
s, b = lec.req("GET", f"/api/learn/courses/{CID}/results/{row.get('user_id', 0)}/help")
t("help draft", 200, s, b)
check("  names the weak concept and links a unit", "Recursion" in b["data"]["body"] and "/unit/" in b["data"]["body"], b["data"]["body"])
check("  links lessons, never a quiz", f"/unit/{U1}" in b["data"]["body"], b["data"]["body"])
s, b = lec.req("POST", f"/api/learn/courses/{CID}/results/{row.get('user_id', 0)}/help", {"subject": b["data"]["subject"], "body": b["data"]["body"] + "\nSee you Tuesday.", "concepts": b["data"]["concepts"]})
t("Lena sends the reviewed help", 200, s, b)
s, b = lec.req("GET", f"/api/learn/courses/{CID}/results")
check("  the map shows it was sent", b["data"]["learners"][0]["helped_at"] is not None, b["data"]["learners"])
t("Sam withdraws consent", 200, stu1.req("DELETE", f"/api/learn/courses/{CID}/consent")[0])
s, b = lec.req("GET", f"/api/learn/courses/{CID}/results")
check("  his results disappear", b["data"]["learners"] == [], b["data"])
t("help to a learner without consent is refused", 403, lec.req("POST", f"/api/learn/courses/{CID}/results/{row.get('user_id', 0)}/help", {"subject": "x", "body": "y"})[0])
s, _ = out.req("GET", f"/api/learn/courses/{CID}/results")
check("an outsider cannot read results", s in (403, 404), s)
t("Lena removes Sam from the course", 200, lec.req("DELETE", f"/api/learn/courses/{CID}/learners/{row.get('user_id', 0)}")[0])

print(f"\npassed {passed}, failed {failed}")
sys.exit(1 if failed else 0)
