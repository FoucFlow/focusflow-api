#!/usr/bin/env bash
# End-to-end test of slide pages (FEATURES.md F3.1) against a running API with
# storage configured. No AI needed. Run from the repo root:
#   BASE=http://localhost:18091 scripts/smoke-slides.sh
# The API needs NEW_COURSES_PER_DAY=0 (or a fresh database).
set -u
B=${BASE:-http://localhost:8091}
DECK=internal/learn/testdata/lecture.pptx
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
printf '%%PDF-1.4\n1 0 obj<</Type/Catalog>>endobj\ntrailer<</Root 1 0 R>>\n%%%%EOF\n' > "$D/notes.pdf"

RUN=$(date +%s)
for u in a:sam b:kim; do
  jar=${u%%:*}; name=${u#*:}
  t "register $name" 201 -b "$D/$jar" -c "$D/$jar" "${J[@]}" -d "{\"name\":\"$name\",\"email\":\"$name$RUN@test.io\",\"password\":\"secret123\",\"password_confirmation\":\"secret123\"}" $B/register
done
t "sam creates a course" 201 "${A[@]}" -F course_title="Recursion" -F course_description="Lecture 3" -F course_category_id=1 $B/api/course
CID=$(py "d['course_id']")

t "upload the lecture .pptx" 201 "${A[@]}" -F "file=@$DECK" -F title="Lecture 3" $B/api/learn/courses/$CID/materials
MID=$(py "d['data']['id']")
t "upload a PDF" 201 "${A[@]}" -F "file=@$D/notes.pdf" $B/api/learn/courses/$CID/materials
PDF=$(py "d['data']['id']")

t "slide pages" 200 "${A[@]}" $B/api/learn/materials/$MID/pages
check "  8 pages in presentation order" "[p['number'] for p in d['data']['pages']]==list(range(1,9)) and d['data']['pages'][7]['title']=='Factorial in Python'"
check "  sections kept" "[p['section'] for p in d['data']['pages']][:4]==['Basics','Basics','Basics','Going deeper']"
check "  text word for word, with bold" "any(r['text']=='base case' and r.get('bold') for l in d['data']['pages'][1]['blocks'][0]['lines'] for r in l['runs'])"
check "  speaker notes" "d['data']['pages'][1]['notes'].startswith('Ask students')"
check "  code block" "d['data']['pages'][7]['blocks'][0]['type']=='code' and 'return n * fact(n - 1)' in d['data']['pages'][7]['blocks'][0]['code']"
check "  table" "d['data']['pages'][3]['blocks'][0]['rows'][2]==['fact(3)','6']"
check "  diagrams flagged (drawing, chart)" "[p['has_diagram'] for p in d['data']['pages']]==[False,False,False,False,True,True,False,False]"
check "  diagram card placed after the text" "[b['type'] for b in d['data']['pages'][4]['blocks']]==['text','diagram']"
check "  hidden slide" "[p['hidden'] for p in d['data']['pages']].count(True)==1 and d['data']['pages'][6]['hidden']"
check "  picture stored with alt text" "d['data']['pages'][2]['blocks'][0]['src'].startswith('http') and d['data']['pages'][2]['blocks'][0]['alt']"
check "  internal fields not exposed" "'media_keys' not in d['data']['pages'][2] and 'Media' not in d['data']['pages'][2]['blocks'][0]"
IMG=$(py "d['data']['pages'][2]['blocks'][0]['src']")
code=$(curl -s -o "$D/img" -w "%{http_code}" "$IMG")
if [ "$code" = 200 ] && [ "$(head -c 4 "$D/img" | tail -c 3)" = PNG ]; then pass=$((pass+1)); echo "ok   picture is served"; else failn=$((failn+1)); echo "FAIL picture is served: $code $IMG"; fi

t "asking again gives the same pages" 200 "${A[@]}" $B/api/learn/materials/$MID/pages
check "  still 8" "len(d['data']['pages'])==8"
t "kim cannot read sam's pages" 403 "${Bo[@]}" $B/api/learn/materials/$MID/pages
t "a PDF has no slide pages" 422 "${A[@]}" $B/api/learn/materials/$PDF/pages
t "unknown material" 404 "${A[@]}" $B/api/learn/materials/99999999/pages

t "delete the .pptx" 200 "${A[@]}" -X DELETE $B/api/learn/materials/$MID
t "  its pages are gone" 404 "${A[@]}" $B/api/learn/materials/$MID/pages
code=$(curl -s -o /dev/null -w "%{http_code}" "$IMG")
if [ "$code" != 200 ]; then pass=$((pass+1)); echo "ok   picture removed from storage ($code)"; else failn=$((failn+1)); echo "FAIL picture still served after delete"; fi

echo; echo "passed $pass, failed $failn"
[ "$failn" = 0 ]
