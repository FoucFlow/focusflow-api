#!/usr/bin/env bash
# Long PDFs read in parts, and units added for newly read pages, against an
# API with AI_PROVIDER=fake and AI_PDF_PART_PAGES=2.
#   BASE=http://localhost:18091 scripts/smoke-parts.sh
set -u
B=${BASE:-http://localhost:8091}
D=$(mktemp -d); trap 'rm -rf "$D"' EXIT
RUN=$(date +%s)$RANDOM
pass=0; failn=0
t() {
  local name=$1 want=$2; shift 2
  local got; got=$(curl -s -o "$D/out" -w "%{http_code}" "$@")
  if [ "$got" = "$want" ]; then pass=$((pass+1)); printf "ok   %-52s %s\n" "$name" "$got"
  else failn=$((failn+1)); printf "FAIL %-52s got %s want %s: %s\n" "$name" "$got" "$want" "$(head -c 300 "$D/out")"; fi
}
py() { python3 -c "import json,sys; d=json.load(open('$D/out')); print($1)"; }
check() { if python3 -c "import json,sys; d=json.load(open('$D/out')); sys.exit(0 if ($2) else 1)"; then pass=$((pass+1)); printf "ok   %s\n" "$1"; else failn=$((failn+1)); printf "FAIL %s: %s\n" "$1" "$(head -c 400 "$D/out")"; fi; }
wait_for() { for _ in $(seq 1 40); do curl -s "${A[@]}" -o "$D/out" "$1"; python3 -c "import json,sys; d=json.load(open('$D/out')); sys.exit(0 if ($2) else 1)" 2>/dev/null && return 0; sleep 1; done; return 1; }
J=(-H "Content-Type: application/json"); A=(-b "$D/a" -c "$D/a")

# A valid 5-page PDF (correct xref offsets)
python3 - "$D/long.pdf" <<'PY'
import sys
n = 5
objs = ["<</Type/Catalog/Pages 2 0 R>>",
        "<</Type/Pages/Kids[%s]/Count %d>>" % (" ".join("%d 0 R" % (3 + 2*i) for i in range(n)), n)]
for i in range(n):
    objs.append("<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 200]/Resources<</Font<</F1<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>>>>>/Contents %d 0 R>>" % (4 + 2*i))
    body = "BT /F1 12 Tf 20 100 Td (Page %d) Tj ET" % (i + 1)
    objs.append("<</Length %d>>stream\n%s\nendstream" % (len(body), body))
out = b"%PDF-1.4\n"; offs = []
for k, o in enumerate(objs, 1):
    offs.append(len(out)); out += ("%d 0 obj\n%s\nendobj\n" % (k, o)).encode()
x = len(out)
out += ("xref\n0 %d\n0000000000 65535 f \n" % (len(objs) + 1)).encode()
out += "".join("%010d 00000 n \n" % o for o in offs).encode()
out += ("trailer\n<</Size %d/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF\n" % (len(objs) + 1, x)).encode()
open(sys.argv[1], "wb").write(out)
PY

t "register" 201 "${A[@]}" "${J[@]}" -d "{\"name\":\"parts\",\"email\":\"parts.$RUN@test.io\",\"password\":\"secret123\",\"password_confirmation\":\"secret123\"}" $B/register
t "create a course" 201 "${A[@]}" -F course_title="Parts $RUN" -F course_category_id=1 $B/api/course
CID=$(py "d['course_id']")
t "add a link" 201 "${A[@]}" "${J[@]}" -d '{"kind":"link","url":"https://go.dev/tour","title":"Go tour"}' $B/api/learn/courses/$CID/materials
t "organise" 202 "${A[@]}" -X POST $B/api/learn/courses/$CID/organise
wait_for $B/api/learn/courses/$CID "d['data']['course']['organise_status']=='ready'" || echo "FAIL organise"
U1=$(py "d['data']['units'][0]['id']")
t "creator edits unit 1" 200 "${A[@]}" -X PUT "${J[@]}" -d '{"title":"My own title"}' $B/api/learn/units/$U1

t "upload a 5-page PDF (read 2 pages at a time)" 201 "${A[@]}" -F "file=@$D/long.pdf" -F title="Long book" $B/api/learn/courses/$CID/materials
MID=$(py "d['data']['id']")
wait_for $B/api/learn/courses/$CID/materials "[m for m in d['data'] if m['id']==$MID][0]['read_pages']==5" || echo "FAIL reading did not finish"
check "  all 5 pages read in parts, no longer reading" "(lambda m: m['total_pages']==5 and not m['reading'] and m['status']=='ready')([m for m in d['data'] if m['id']==$MID][0])"
check "  concepts from all 3 parts, page numbers of the whole book" "(lambda c: len(c)==6 and c[2]['pages']==[3,4] and c[4]['pages'][0]==5)([m for m in d['data'] if m['id']==$MID][0]['digest']['concepts'])"
wait_for $B/api/learn/courses/$CID "d['data']['course']['organise_status']=='ready' and not d['data']['course']['organise_more'] and len(d['data']['units'])>=2" || echo "FAIL units not added"
check "  new units added after unit 1" "len(d['data']['units'])>=2 and d['data']['units'][0]['id']==$U1"
check "  unit 1 kept the creator's edit" "d['data']['units'][0]['title']=='My own title'"
check "  every concept of the book is in a unit" "len([c for u in d['data']['units'][1:] for c in u['concepts']])==6"
check "  organise runs not used up by added units" "d['data']['course']['organise_runs']==1"
t "materials list" 200 "${A[@]}" $B/api/learn/courses/$CID/materials
check "  all concepts marked organised" "all(c.get('organised') for m in d['data'] for c in (m['digest']['concepts'] or []))"
t "a fully read PDF cannot be retried" 422 "${A[@]}" -X POST $B/api/learn/materials/$MID/retry

echo "passed $pass, failed $failn"
[ "$failn" = 0 ]
