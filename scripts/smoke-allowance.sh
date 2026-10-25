#!/usr/bin/env bash
# Each creator's AI allowance, against an API with AI_PROVIDER=fake (every
# fake call costs $0.001) and AI_USER_DAILY_USD=0.003, on an EMPTY database.
#   BASE=http://localhost:18091 scripts/smoke-allowance.sh
set -u
B=${BASE:-http://localhost:8091}
D=$(mktemp -d); trap 'rm -rf "$D"' EXIT
pass=0; failn=0
t() {
  local name=$1 want=$2; shift 2
  local got; got=$(curl -s -o "$D/out" -w "%{http_code}" "$@")
  if [ "$got" = "$want" ]; then pass=$((pass+1)); printf "ok   %-52s %s\n" "$name" "$got"
  else failn=$((failn+1)); printf "FAIL %-52s got %s want %s: %s\n" "$name" "$got" "$want" "$(head -c 240 "$D/out")"; fi
}
py() { python3 -c "import json,sys; d=json.load(open('$D/out')); print($1)"; }
check() { if python3 -c "import json,sys; d=json.load(open('$D/out')); sys.exit(0 if ($2) else 1)"; then pass=$((pass+1)); printf "ok   %s\n" "$1"; else failn=$((failn+1)); printf "FAIL %s: %s\n" "$1" "$(head -c 300 "$D/out")"; fi; }
J=(-H "Content-Type: application/json")
A=(-b "$D/a" -c "$D/a"); Bo=(-b "$D/b" -c "$D/b")
wait_ready() { # all of the course's materials read
  for _ in $(seq 1 40); do
    curl -s "${A[@]}" -o "$D/out" "$B/api/learn/courses/$1/materials"
    python3 -c "import json,sys; d=json.load(open('$D/out')); sys.exit(0 if all(m['status']!='processing' for m in d['data']) else 1)" && return 0
    sleep 0.5
  done; return 1
}

printf '%%PDF-1.4\n1 0 obj<</Type/Catalog>>endobj\ntrailer<</Root 1 0 R>>\n%%%%EOF\n' > "$D/notes.pdf"

for u in a:alice b:bob; do
  jar=${u%%:*}; name=${u#*:}
  t "register $name" 201 -b "$D/$jar" -c "$D/$jar" "${J[@]}" -d "{\"name\":\"$name\",\"email\":\"$name@allow.io\",\"password\":\"secret123\",\"password_confirmation\":\"secret123\"}" $B/register
done

t "/me before any AI work" 200 "${A[@]}" $B/api/learn/me
check "  0% used today and this month" "(lambda a: a['can_use_ai'] and not a['unlimited'] and [w['period'] for w in a['windows']]==['day','month'] and all(w['used_percent']==0 for w in a['windows']))(d['data']['ai_allowance'])"

t "alice creates a course" 201 "${A[@]}" -F course_title="Allowance" -F course_description="x" -F course_category_id=1 $B/api/course
CID=$(py "d['course_id']")
t "upload PDF 1 (one AI call)" 201 "${A[@]}" -F "file=@$D/notes.pdf" -F title="A" $B/api/learn/courses/$CID/materials
wait_ready $CID
t "builder view shows the allowance" 200 "${A[@]}" $B/api/learn/courses/$CID
check "  33% of today's allowance used" "d['data']['ai_allowance']['windows'][0]['used_percent']==33 and d['data']['ai_allowance']['can_use_ai']"
t "upload PDF 2" 201 "${A[@]}" -F "file=@$D/notes.pdf" -F title="B" $B/api/learn/courses/$CID/materials
wait_ready $CID
t "upload PDF 3" 201 "${A[@]}" -F "file=@$D/notes.pdf" -F title="C" $B/api/learn/courses/$CID/materials
wait_ready $CID
t "/me after three AI calls" 200 "${A[@]}" $B/api/learn/me
check "  100% used, AI work refused with when it refills" "(lambda a: not a['can_use_ai'] and a['windows'][0]['used_percent']==100 and 'refills in' in a['message'])(d['data']['ai_allowance'])"
t "a 4th material is refused" 422 "${A[@]}" -F "file=@$D/notes.pdf" -F title="D" $B/api/learn/courses/$CID/materials
check "  says today's allowance is used" "'today' in json.dumps(d)"
t "no upload token either" 422 "${A[@]}" -X POST $B/api/learn/courses/$CID/upload-url
t "organise is refused" 422 "${A[@]}" -X POST $B/api/learn/courses/$CID/organise
t "editing by hand still works" 201 "${A[@]}" "${J[@]}" -d '{"title":"My own unit"}' $B/api/learn/courses/$CID/units

t "bob has his own allowance" 200 "${Bo[@]}" $B/api/learn/me
check "  bob still at 0%" "d['data']['ai_allowance']['can_use_ai'] and d['data']['ai_allowance']['windows'][0]['used_percent']==0"

echo "passed $pass, failed $failn"
[ $failn -eq 0 ]
