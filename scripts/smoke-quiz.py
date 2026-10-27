#!/usr/bin/env python3
"""End-to-end test of quizzes (FEATURES.md M2: F2.1-F2.12) against a running
API with storage and AI_PROVIDER=fake. No third-party packages. Run from the
repo root:
    BASE=http://localhost:18091 python3 scripts/smoke-quiz.py
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
sam, kim, lee = User("sam"), User("kim"), User("lee")
for u in (sam, kim, lee):
    s, b = u.req("POST", "/register", {"name": u.name, "email": f"{u.name}{run}q@test.io", "password": "secret123", "password_confirmation": "secret123"})
    t(f"register {u.name}", 201, s, b)

# ── course with two slide units ────────────────────────────────────────────
s, b = sam.req("POST", "/api/course", form={"course_title": "Quiz course", "course_description": "x", "course_category_id": "1", "build_mode": "slides"})
t("sam creates a slides course", 201, s, b)
CID = b["course_id"]
s, b = sam.req("POST", f"/api/learn/courses/{CID}/materials", form={"title": "Lecture"}, files={"file": ("lecture.pptx", open(DECK, "rb").read(), "application/vnd.openxmlformats-officedocument.presentationml.presentation")})
t("upload deck", 201, s, b)
MID = b["data"]["id"]
s, b = sam.req("PUT", f"/api/learn/courses/{CID}/slide-units", {"units": [
    {"title": "Basics", "material_id": MID, "first_page": 1, "last_page": 3},
    {"title": "Deeper", "material_id": MID, "first_page": 4, "last_page": 8}]})
t("cut into two units", 200, s, b)
U1, U2 = b["data"][0]["id"], b["data"][1]["id"]

# ── unit test on unit 1: import ────────────────────────────────────────────
s, b = sam.req("GET", "/api/learn/quiz-template.csv")
t("download the CSV template", 200, s, b)
template = b.decode() if isinstance(b, bytes) else ""
s, b = sam.req("POST", f"/api/learn/units/{U1}/questions/import", {"format": "csv", "text": template})
t("preview the template", 200, s, b)
check("  5 questions, no errors, nothing saved", len(b["data"]["questions"]) == 5 and not b["data"]["errors"] and b["data"]["saved"] == 0, b)
bad = "type,question,option_a,option_b,correct\nmcq,ok?,a,b,A\nmcq,bad?,a,b,Z\n"
s, b = sam.req("POST", f"/api/learn/units/{U1}/questions/import", {"format": "csv", "text": bad, "save": True})
t("save refused while a line is wrong", 422, s, b)
check("  the error names line 3", b["data"]["errors"][0]["line"] == 3, b)
s, b = sam.req("POST", f"/api/learn/units/{U1}/questions/import", {"format": "csv", "text": template, "save": True})
t("save the template questions", 201, s, b)
gift = "::a:: Pick right {=right ~wrong ~other}\n\n::b:: Sky is green. {F}\n"
s, b = sam.req("POST", f"/api/learn/units/{U1}/questions/import", {"format": "gift", "text": gift, "save": True})
t("import GIFT", 201, s, b)
s, b = sam.req("POST", f"/api/learn/units/{U1}/questions/import", {"format": "aiken", "text": "Two plus two?\nA. 3\nB. 4\nANSWER: B\n", "save": True})
t("import Aiken", 201, s, b)
s, b = sam.req("POST", f"/api/learn/units/{U1}/questions", {"kind": "multi", "prompt": "Pick the even ones", "options": ["2", "3", "4"], "corrects": [0, 2]})
t("write a multiple-select question", 201, s, b)
s, b = sam.req("POST", f"/api/learn/units/{U1}/questions", {"kind": "multi", "prompt": "No right one", "options": ["a", "b"], "corrects": []})
t("multiple select needs a right option", 422, s, b)
s, b = sam.req("PUT", f"/api/learn/units/{U1}/test", {"enabled": True, "level": "standard", "questions_per_test": 4, "time_limit_minutes": 0, "pass_mark": 50})
t("turn the unit test on (standard)", 200, s, b)
check("  level stays standard", b["data"]["level"] == "standard", b)
s, b = sam.req("PUT", f"/api/learn/units/{U1}/test", {"max_attempts": 5})
check("  a change makes it custom", s == 200 and b["data"]["level"] == "custom", b)
s, b = sam.req("PUT", f"/api/learn/units/{U1}/test", {"level": "standard", "time_limit_minutes": 0, "pass_mark": 50})
check("  back to standard", b["data"]["level"] == "standard", b)

# ── quizzes placed in the course ───────────────────────────────────────────
s, b = sam.req("POST", f"/api/learn/courses/{CID}/quizzes", {"title": "Check-in", "after_unit_id": U1, "practice": True})
t("practice quiz after unit 1", 201, s, b)
QP = b["data"]["id"]
s, b = sam.req("POST", f"/api/learn/courses/{CID}/quizzes", {"final": True})
t("final exam at the end", 201, s, b)
QF = b["data"]["id"]
s, b = sam.req("GET", f"/api/learn/courses/{CID}")
order = [(u["title"], u["kind"]) for u in b["data"]["units"]]
check("  order: Basics, Check-in, Deeper, Final exam", order == [("Basics", "lesson"), ("Check-in", "quiz"), ("Deeper", "lesson"), ("Final exam", "quiz")], order)
t("only one final exam", 409, sam.req("POST", f"/api/learn/courses/{CID}/quizzes", {"final": True})[0])
ids = [u["id"] for u in b["data"]["units"]]
s, b = sam.req("PUT", f"/api/learn/courses/{CID}/units/order", {"unit_ids": [ids[3], ids[0], ids[1], ids[2]]})
check("the final exam stays last when units move", s == 200 and b["data"][-1]["id"] == QF, [u["title"] for u in b["data"]] if s == 200 else b)
s, b = sam.req("POST", f"/api/learn/units/{QP}/questions/generate")
t("AI writes the practice quiz (from slides)", 202, s, b)
ok = wait(lambda: sam.req("GET", f"/api/learn/units/{QP}/test")[1]["data"]["settings"]["pool_status"] == "ready")
s, b = sam.req("GET", f"/api/learn/units/{QP}/test")
kinds = {q["kind"] for q in b["data"]["questions"]}
check("  questions written, incl. multi and true/false", ok and {"mcq", "multi", "truefalse"} <= kinds, kinds)
check("  from the slides (concepts are slide titles)", any(q["concept"] == "Why we need a base case" for q in b["data"]["questions"]), [q["concept"] for q in b["data"]["questions"]][:5])
s, b = sam.req("PUT", f"/api/learn/units/{QP}/test", {"enabled": True})
t("practice quiz on", 200, s, b)
check("  practice level", b["data"]["level"] == "practice" and b["data"]["practice"], b)
s, b = sam.req("POST", f"/api/learn/units/{QF}/questions/generate")
t("AI writes the final exam (whole course)", 202, s, b)
wait(lambda: sam.req("GET", f"/api/learn/units/{QF}/test")[1]["data"]["settings"]["pool_status"] == "ready")
s, b = sam.req("PUT", f"/api/learn/units/{QF}/test", {"enabled": True, "level": "supervised", "camera": True, "questions_per_test": 3})
t("final exam: supervised + camera", 200, s, b)
CODE = b["data"]["access_code"]
check("  access code and explain-your-answer on", len(CODE) == 6 and b["data"]["explain_answers"] and b["data"]["teacher_review"], b)
s, b = sam.req("PUT", f"/api/learn/units/{QF}/test", {"opens_at": "2030-01-01T09:00:00Z", "closes_at": "2029-01-01T09:00:00Z"})
t("closing before opening is refused", 422, s, b)

s, b = sam.req("POST", f"/api/learn/courses/{CID}/publish", {"visibility": "public", "confirm_rights": True})
t("publish", 200, s, b)

# ── learner ────────────────────────────────────────────────────────────────
t("kim joins", 200, kim.req("POST", f"/api/learn/courses/{CID}/join")[0])
s, b = kim.req("GET", f"/api/learn/courses/{CID}")
check("  3 required units (practice quiz does not count)", b["data"]["total_units"] == 3, b["data"]["total_units"])
s, b = kim.req("POST", f"/api/learn/units/{U1}/complete")
t("unit 1 cannot be marked done (test on)", 403, s, b)


def take(user, uid, right=True, code=None, extra=None):
    s, b = user.req("POST", f"/api/learn/units/{uid}/test/start", {"access_code": code} if code else {})
    if s != 200:
        return s, b
    a = b["data"]
    _, full = sam.req("GET", f"/api/learn/attempts/{a['id']}")  # the creator sees the right answers
    answers = []
    for i, it in enumerate(full["data"]["attempt"]["items"]):
        if it["kind"] == "multi":
            answers.append({"index": i, "choices": it["corrects"] if right else []})
        elif it["kind"] in ("mcq", "truefalse"):
            answers.append({"index": i, "choice": it["correct"] if right else (it["correct"] + 1) % len(it["options"])})
        else:
            answers.append({"index": i, "answer": "correct" if right else "no"})
    if extra:
        extra(a)
    return user.req("POST", f"/api/learn/attempts/{a['id']}/submit", {"answers": answers})


s, b = take(kim, U1, right=False)
t("kim fails the unit test", 200, s, b)
done = wait(lambda: kim.req("GET", f"/api/learn/attempts/{b['data']['id']}")[1]["data"]["status"] == "done")
s, b = kim.req("GET", f"/api/learn/attempts/{b['data']['id']}")
check("  failed; right answers stay hidden (attempts left)", not b["data"]["passed"] and not any("correct" in it or "corrects" in it for it in b["data"]["items"]), b["data"])
t("unit 2 still locked", 403, kim.req("GET", f"/api/learn/units/{U2}/pages")[0])
s, b = take(kim, U1)
t("kim passes on the second attempt", 200, s, b)
wait(lambda: kim.req("GET", f"/api/learn/attempts/{b['data']['id']}")[1]["data"]["status"] == "done")
s, b = kim.req("GET", f"/api/learn/attempts/{b['data']['id']}")
check("  passed: now the right answers are shown", b["data"]["passed"] and all("correct" in it for it in b["data"]["items"] if it["kind"] in ("mcq", "truefalse")), b["data"])
t("practice quiz opens", 200, kim.req("GET", f"/api/learn/units/{QP}/test")[0])
t("unit 2 opens without the practice quiz", 200, kim.req("GET", f"/api/learn/units/{U2}/pages")[0])
s, b = kim.req("POST", f"/api/learn/units/{QP}/test/start")
t("start the practice quiz", 200, s, b)
pick = next(i for i, it in enumerate(b["data"]["items"]) if it["kind"] in ("mcq", "truefalse", "multi"))
s, c = kim.req("POST", f"/api/learn/attempts/{b['data']['id']}/check", {"index": pick})
t("practice: check an answer right away", 200, s, c)
check("  the right answer comes back", "correct" in c["data"], c)
s, b = take(kim, U1)
check("  a passed unit test cannot be taken again", s == 422, b)
s, b = kim.req("POST", f"/api/learn/units/{U2}/complete")
t("finish unit 2", 200, s, b)
check("  not finished yet (final exam left)", not b["course_completed"], b)

# final exam: access code, camera, explain, teacher review, certificate
s, b = kim.req("POST", f"/api/learn/units/{QF}/test/start", {})
t("final exam needs the access code", 422, s, b)
PNG = bytes.fromhex("89504e470d0a1a0a0000000d4948445200000001000000010806000000" "1f15c4890000000d49444154789c6360000002000154a24f5d0000000049454e44ae426082")


def snap(a):
    s, _ = kim.req("POST", f"/api/learn/attempts/{a['id']}/snapshots", files={"image": ("s.png", PNG, "image/png")})
    t("  camera picture sent", 204, s)


s, b = take(kim, QF, code=CODE.lower(), extra=snap)
t("kim sits the final exam (code accepted, any case)", 200, s, b)
AID = b["data"]["id"]
fu = wait(lambda: (lambda d: d["status"] == "explain" and d.get("followups"))(kim.req("GET", f"/api/learn/attempts/{AID}")[1]["data"]))
check("  follow-up questions about own answers", bool(fu), kim.req("GET", f"/api/learn/attempts/{AID}")[1])
s, b = kim.req("POST", f"/api/learn/attempts/{AID}/explain", {"answers": ["x"] * 9})
t("  all replies needed (wrong count)", 422, s, b)
n = len(kim.req("GET", f"/api/learn/attempts/{AID}")[1]["data"]["followups"])
s, b = kim.req("POST", f"/api/learn/attempts/{AID}/explain", {"answers": ["Because the base case stops it."] * n})
t("  kim explains", 202, s, b)
st = wait(lambda: kim.req("GET", f"/api/learn/attempts/{AID}")[1]["data"]["status"] == "review")
check("  then waits for the teacher", bool(st), kim.req("GET", f"/api/learn/attempts/{AID}")[1]["data"]["status"])
s, b = sam.req("GET", f"/api/learn/attempts/{AID}")
check("  teacher sees consistency + evidence", b["data"]["attempt"]["consistency"] == "consistent" and "snapshots" not in b["data"]["attempt"], b["data"]["attempt"].get("consistency"))
s, b = sam.req("GET", f"/api/learn/courses/{CID}/attempts?status=review")
check("  review list shows 1 camera picture", b["data"][0]["snapshots"] == 1, b["data"])
s, img = sam.req("GET", f"/api/learn/attempts/{AID}/snapshots/0")
check("  teacher opens the picture", s == 200 and isinstance(img, bytes) and img[:4] == b"\x89PNG", s)
t("  kim cannot open it", 403, kim.req("GET", f"/api/learn/attempts/{AID}/snapshots/0")[0])
t("  lee cannot open it", 404, lee.req("GET", f"/api/learn/attempts/{AID}/snapshots/0")[0])
items = sam.req("GET", f"/api/learn/attempts/{AID}")[1]["data"]["attempt"]["items"]
s, b = sam.req("POST", f"/api/learn/attempts/{AID}/review", {"marks": [{"index": i, "mark": 1} for i in range(len(items))]})
t("teacher confirms the marks", 200, s, b)
s, b = kim.req("GET", f"/api/learn/courses/{CID}")
cert = b["data"].get("certificate")
check("course finished: certificate issued", cert and cert["code"] and cert["final_score"] == 100, b["data"].get("certificate"))
s, b = lee.req("GET", f"/api/certificates/{cert['code'] if cert else 'X'}")
t("anyone can verify the certificate", 200, s, b)
check("  shows learner and course", b["data"]["learner_name"] == "kim" and b["data"]["course_title"] == "Quiz course", b)
t("an unknown code", 404, lee.req("GET", "/api/certificates/NOPE123")[0])

# re-cutting the slides keeps the quizzes in place
s, b = sam.req("POST", f"/api/learn/courses/{CID}/unpublish")
s, b = sam.req("PUT", f"/api/learn/courses/{CID}/slide-units", {"units": [
    {"id": U1, "title": "Basics", "material_id": MID, "first_page": 1, "last_page": 2},
    {"id": U2, "title": "Deeper", "material_id": MID, "first_page": 3, "last_page": 8}]})
t("re-cut the slides", 200, s, b)
order = [(u["title"], u["kind"]) for u in b["data"]]
check("  quizzes kept their places", order == [("Basics", "lesson"), ("Check-in", "quiz"), ("Deeper", "lesson"), ("Final exam", "quiz")], order)

print(f"\npassed {passed}, failed {failed}")
sys.exit(1 if failed else 0)
