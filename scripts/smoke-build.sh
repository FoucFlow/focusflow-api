#!/usr/bin/env bash
# The course builds itself in the background within the creator's AI
# allowance, against an API with AI_PROVIDER=fake (each call $0.001),
# AI_PDF_PART_PAGES=5, AI_PDF_FIRST_PART_PAGES=2, AI_USER_DAILY_USD=0.008.
# Run from the repo root (uses internal/learn/testdata/five-pages.pdf).
#   BASE=http://localhost:18091 scripts/smoke-build.sh
set -u
B=${BASE:-http://localhost:8091}
D=$(mktemp -d); trap 'rm -rf "$D"' EXIT
RUN=$(date +%s)$RANDOM
BOOK=internal/learn/testdata/five-pages.pdf
pass=0; failn=0
t() {
  local name=$1 want=$2; shift 2
  local got; got=$(curl -s -o "$D/out" -w "%{http_code}" "$@")
  if [ "$got" = "$want" ]; then pass=$((pass+1)); printf "ok   %-52s %s\n" "$name" "$got"
  else failn=$((failn+1)); printf "FAIL %-52s got %s want %s: %s\n" "$name" "$got" "$want" "$(head -c 240 "$D/out")"; fi
}
py() { python3 -c "import json,sys; d=json.load(open('$D/out')); print($1)"; }
check() { if python3 -c "import json,sys; d=json.load(open('$D/out')); sys.exit(0 if ($2) else 1)"; then pass=$((pass+1)); printf "ok   %s\n" "$1"; else failn=$((failn+1)); printf "FAIL %s: %s\n" "$1" "$(head -c 400 "$D/out")"; fi; }
J=(-H "Content-Type: application/json")
A=(-b "$D/a" -c "$D/a")
wait_built() { # nothing generating and nothing being read (builder view in $D/out)
  for _ in $(seq 1 60); do
    curl -s "${A[@]}" -o "$D/out" "$B/api/learn/courses/$1"
    python3 -c "import json,sys; d=json.load(open('$D/out'))['data']; p=d['progress']; sys.exit(0 if p and not p['active'] and not any(m['status']=='processing' or m['reading'] for m in d['materials']) else 1)" && return 0
    sleep 0.5
  done; return 1
}

t "register" 201 "${A[@]}" "${J[@]}" -d "{\"name\":\"Builder\",\"email\":\"b$RUN@build.io\",\"password\":\"secret123\",\"password_confirmation\":\"secret123\"}" $B/register
t "create a course" 201 "${A[@]}" -F course_title="Build $RUN" -F course_description="x" -F course_category_id=1 $B/api/course
CID=$(py "d['course_id']")
t "builder view before any material" 200 "${A[@]}" $B/api/learn/courses/$CID
check "  no progress bar yet" "d['data']['progress'] is None"

t "upload a 5-page book" 201 "${A[@]}" -F "file=@$BOOK" -F title="Book" $B/api/learn/courses/$CID/materials
wait_built $CID || echo "FAIL the book was not built"
check "  first part 2 pages, then 3: whole book read" "(lambda m: m['read_pages']==5 and m['total_pages']==5)(d['data']['materials'][0])"
check "  organised by itself, one run" "len(d['data']['units'])>=1 and d['data']['course']['organise_runs']==1 and d['data']['progress']['percent']==100"
check "  75% of today's allowance used" "d['data']['ai_allowance']['windows'][0]['used_percent']==75"

t "add a second book (25% left)" 201 "${A[@]}" -F "file=@$BOOK" -F title="Second" $B/api/learn/courses/$CID/materials
SID=$(py "d['data']['id']")
wait_built $CID
check "  it waits instead of using up the allowance" "(lambda m: m['status']=='paused' and 'Waiting for your AI allowance' in m['error'])([m for m in d['data']['materials'] if m['id']==$SID][0])"
check "  course ready with what was read; the rest later" "(lambda p: p['stage']=='ready' and p['later'] and 'refills' in p['label'])(d['data']['progress'])"
check "  allowance unchanged" "d['data']['ai_allowance']['windows'][0]['used_percent']==75"
t "Read now reads it anyway" 202 "${A[@]}" -X POST $B/api/learn/materials/$SID/retry
wait_built $CID
check "  read, and new units added" "(lambda m: m['status']=='ready' and m['read_pages']==5)([m for m in d['data']['materials'] if m['id']==$SID][0]) and d['data']['course']['organise_runs']==1"

echo "passed $pass, failed $failn"
[ $failn -eq 0 ]
