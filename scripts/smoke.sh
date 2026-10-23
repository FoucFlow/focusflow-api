#!/usr/bin/env bash
# End-to-end smoke test against a running API with an EMPTY database.
#   BASE=http://localhost:8091 ADMIN_EMAIL=.. ADMIN_PASSWORD=.. scripts/smoke.sh
set -u
B=${BASE:-http://localhost:8091}
D=$(mktemp -d); trap 'rm -rf "$D"' EXIT
pass=0; failn=0
# t <name> <expected-status> curl-args...
t() {
  local name=$1 want=$2; shift 2
  local got; got=$(curl -s -o "$D/out" -w "%{http_code}" "$@")
  if [ "$got" = "$want" ]; then pass=$((pass+1)); printf "ok   %-46s %s\n" "$name" "$got"
  else failn=$((failn+1)); printf "FAIL %-46s got %s want %s: %s\n" "$name" "$got" "$want" "$(head -c 200 "$D/out")"; fi
}
J=(-H "Content-Type: application/json")
A=(-b "$D/a" -c "$D/a"); Bo=(-b "$D/b" -c "$D/b"); AD=(-b "$D/adm" -c "$D/adm")
jq_() { python3 -c "import sys,json; d=json.load(open('$D/out')); print(eval(sys.argv[1]))" "$1"; }

t "health" 200 $B/health
t "register alice" 201 "${A[@]}" "${J[@]}" -d '{"name":"Alice","email":"Alice@Test.io","password":"secret123","password_confirmation":"secret123"}' $B/register
ALICE=$(jq_ "d['user_id']")
t "register duplicate email" 422 "${J[@]}" -d '{"name":"A2","email":"alice@test.io","password":"secret123","password_confirmation":"secret123"}' $B/register
t "register short password" 422 "${J[@]}" -d '{"name":"C","email":"c@test.io","password":"123","password_confirmation":"123"}' $B/register
t "register bob" 201 "${Bo[@]}" "${J[@]}" -d '{"name":"Bob","email":"bob@test.io","password":"secret123","password_confirmation":"secret123"}' $B/register
BOB=$(jq_ "d['user_id']")
t "me with cookie" 200 "${A[@]}" $B/api/user
t "me without cookie" 401 $B/api/user
t "login wrong password" 422 "${J[@]}" -d '{"email":"alice@test.io","password":"nope"}' $B/login
t "login ok" 200 "${A[@]}" "${J[@]}" -d '{"email":"alice@test.io","password":"secret123"}' $B/login
t "edit own profile" 200 "${A[@]}" -X PUT "${J[@]}" -d '{"bio":"hi","username":"alice"}' $B/api/user/$ALICE
t "edit someone else" 403 "${Bo[@]}" -X PUT "${J[@]}" -d '{"bio":"hacked"}' $B/api/user/$ALICE
t "taken username" 422 "${Bo[@]}" -X PUT "${J[@]}" -d '{"username":"alice"}' $B/api/user/$BOB
t "show user needs login" 401 $B/api/user/$ALICE
t "lookup by email" 200 "${A[@]}" "$B/api/user/lookup?email=bob@test.io"
t "student on admin route" 403 "${A[@]}" $B/api/admin/users
t "change password wrong current" 422 "${Bo[@]}" -X PUT "${J[@]}" -d '{"current_password":"x","password":"newpass123","password_confirmation":"newpass123"}' $B/api/user/$BOB/password
t "change password" 200 "${Bo[@]}" -X PUT "${J[@]}" -d '{"current_password":"secret123","password":"newpass123","password_confirmation":"newpass123"}' $B/api/user/$BOB/password
t "forgot password (unknown email)" 200 "${J[@]}" -d '{"email":"nobody@test.io"}' $B/forgot-password
t "reset with bad token" 422 "${J[@]}" -d '{"token":"bad","email":"bob@test.io","password":"x2345678","password_confirmation":"x2345678"}' $B/reset-password

if [ -n "${ADMIN_EMAIL:-}" ]; then
  t "admin login" 200 "${AD[@]}" "${J[@]}" -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASSWORD\"}" $B/login
  ADMIN=$(jq_ "d['user_id']")
  t "admin list users" 200 "${AD[@]}" "$B/api/admin/users?per_page=2"
  t "admin make alice teacher" 200 "${AD[@]}" -X PUT "${J[@]}" -d '{"role":"teacher"}' $B/api/admin/users/$ALICE/role
  t "admin change own role" 403 "${AD[@]}" -X PUT "${J[@]}" -d '{"role":"student"}' $B/api/admin/users/$ADMIN/role
  t "admin user detail" 200 "${AD[@]}" $B/api/admin/users/$ALICE
  t "admin stats" 200 "${AD[@]}" $B/api/admin/stats
  t "analytics overview" 200 "${AD[@]}" $B/api/admin/analytics/overview
  t "analytics growth" 200 "${AD[@]}" "$B/api/admin/analytics/users/growth?period=weekly"
  t "analytics activity" 200 "${AD[@]}" $B/api/admin/analytics/users/activity
  t "analytics roles" 200 "${AD[@]}" $B/api/admin/analytics/users/roles
  t "export csv" 200 "${AD[@]}" "$B/api/admin/analytics/reports/export?format=csv&type=activity"
  t "course analytics overview" 200 "${AD[@]}" $B/api/admin/analytics/courses/overview
  t "material analytics" 200 "${AD[@]}" $B/api/analytics/materials/stats
fi

t "list courses (public)" 200 $B/api/course
t "create course" 201 "${A[@]}" -F course_title=Go101 -F course_description=Intro -F course_category_id=1 -F course_visibility=public $B/api/course
COURSE=$(jq_ "(d.get('data') or d).get('course_id')")
t "get course" 200 $B/api/course/$COURSE
t "bob edits alice course" 403 "${Bo[@]}" -X PUT -F course_title=Hacked $B/api/course/$COURSE
t "alice invites bob (no SMTP: logged)" 200 "${A[@]}" "${J[@]}" -d '{"email":"bob@test.io"}' $B/api/course/$COURSE/invite-email
t "bob enrolls" 201 "${Bo[@]}" -X POST $B/api/course/$COURSE/enroll
t "my enrollments" 200 "${Bo[@]}" $B/api/enrollments
t "bob notifications" 200 "${Bo[@]}" $B/api/notifications
t "lesson create by owner" 201 "${A[@]}" "${J[@]}" -d "[{\"course_id\":\"$COURSE\",\"youtube_id\":\"dQw4w9WgXcQ\",\"title\":\"L1\",\"lesson_id\":\"l1\"}]" $B/api/course-materials
t "lesson create by non-owner" 403 "${Bo[@]}" "${J[@]}" -d "[{\"course_id\":\"$COURSE\",\"youtube_id\":\"x\",\"title\":\"L2\"}]" $B/api/course-materials
t "lessons of course" 200 "${Bo[@]}" $B/api/course-materials/course/$COURSE
t "lessons need login" 401 $B/api/course-materials/course/$COURSE
t "dashboard (session user)" 200 "${Bo[@]}" $B/api/dashboard/summary
# 503 when GEMINI_API_KEY is not configured (quiz feature off)
t "quiz attempts (session user)" "${QUIZ_STATUS:-200}" "${Bo[@]}" $B/api/quiz-attempts
t "logout" 200 "${A[@]}" -X POST $B/logout
t "me after logout" 401 "${A[@]}" $B/api/user

echo "passed $pass, failed $failn"
[ "$failn" = 0 ]
