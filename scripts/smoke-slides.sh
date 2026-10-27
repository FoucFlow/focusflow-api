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
A=(-b "$D/a" -c "$D/a"); Bo=(-b "$D/b" -c "$D/b"); C=(-b "$D/c" -c "$D/c")
printf '%%PDF-1.4\n1 0 obj<</Type/Catalog>>endobj\ntrailer<</Root 1 0 R>>\n%%%%EOF\n' > "$D/notes.pdf"

RUN=$(date +%s)
for u in a:sam b:kim c:lee; do
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

PID=$(py "d['data']['pages'][1]['id']")
t "edit a slide (F3.6)" 200 "${A[@]}" "${J[@]}" -X PUT -d '{"title":"Base case","blocks":[{"type":"text","lines":[{"level":0,"bullet":true,"runs":[{"text":"Edited","bold":true},{"text":" line","link":"javascript:alert(1)"}]}]}],"diagram":true}' $B/api/learn/slide-pages/$PID
check "  saved, unsafe link dropped, diagram card added" "d['data']['edited'] and d['data']['title']=='Base case' and d['data']['blocks'][0]['lines'][0]['runs'][1].get('link') is None and d['data']['has_diagram']"
t "  a picture from elsewhere is refused" 422 "${A[@]}" "${J[@]}" -X PUT -d '{"blocks":[{"type":"image","src":"https://evil.example/x.png"}]}' $B/api/learn/slide-pages/$PID
t "  kim cannot edit sam's slide" 403 "${Bo[@]}" "${J[@]}" -X PUT -d '{"title":"x"}' $B/api/learn/slide-pages/$PID
t "  hide it" 200 "${A[@]}" "${J[@]}" -X PUT -d '{"hidden":true}' $B/api/learn/slide-pages/$PID
t "asking again gives the same pages" 200 "${A[@]}" $B/api/learn/materials/$MID/pages
check "  still 8, the edit kept" "len(d['data']['pages'])==8 and d['data']['pages'][1]['title']=='Base case' and d['data']['pages'][1]['hidden']"
t "kim cannot read sam's pages" 403 "${Bo[@]}" $B/api/learn/materials/$MID/pages
t "a PDF has no slide pages" 422 "${A[@]}" $B/api/learn/materials/$PDF/pages
t "unknown material" 404 "${A[@]}" $B/api/learn/materials/99999999/pages

t "delete the .pptx" 200 "${A[@]}" -X DELETE $B/api/learn/materials/$MID
t "  its pages are gone" 404 "${A[@]}" $B/api/learn/materials/$MID/pages
code=$(curl -s -o /dev/null -w "%{http_code}" "$IMG")
if [ "$code" != 200 ]; then pass=$((pass+1)); echo "ok   picture removed from storage ($code)"; else failn=$((failn+1)); echo "FAIL picture still served after delete"; fi

# ── Slides as written: build mode, cutting into units, learners (F1.10, F3.3, F3.7)
python3 - "$D" <<'PY'
import sys, zipfile
with zipfile.ZipFile(sys.argv[1] + "/blank.pptx", "w") as z:
    z.writestr("ppt/slides/slide1.xml", '<p:sld xmlns:p="p" xmlns:a="a"><p:cSld><p:spTree/></p:cSld></p:sld>')
PY
t "sam creates a slides course" 201 "${A[@]}" -F course_title="Recursion as written" -F course_description="Lecture 3" -F course_category_id=1 -F build_mode=slides $B/api/course
SC=$(py "d['course_id']")
t "builder view" 200 "${A[@]}" $B/api/learn/courses/$SC
check "  build mode is slides" "d['data']['course']['build_mode']=='slides'"
t "upload the lecture" 201 "${A[@]}" -F "file=@$DECK" -F title="Lecture 3" $B/api/learn/courses/$SC/materials
SM=$(py "d['data']['id']")
check "  ready at once, no AI digest" "d['data']['status']=='ready' and not d['data']['digest']['summary']"
t "a deck with no text is fine here" 201 "${A[@]}" -F "file=@$D/blank.pptx" $B/api/learn/courses/$SC/materials
BLANK=$(py "d['data']['id']")
t "  (remove it)" 200 "${A[@]}" -X DELETE $B/api/learn/materials/$BLANK
t "a video is not watched by the AI" 201 "${A[@]}" "${J[@]}" -d '{"kind":"video","url":"https://youtu.be/dQw4w9WgXcQ","title":"Intro"}' $B/api/learn/courses/$SC/materials
check "  ready at once" "d['data']['status']=='ready'"
t "AI organise is refused" 422 "${A[@]}" -X POST $B/api/learn/courses/$SC/organise
check "  says to cut the slides" "'slides as written' in d['message']"
t "publish needs units" 422 "${A[@]}" "${J[@]}" -d '{"visibility":"public","confirm_rights":true}' $B/api/learn/courses/$SC/publish
t "AI suggests units (F3.5)" 200 "${A[@]}" -X POST $B/api/learn/materials/$SM/suggest-units
check "  cuts start at slide 1, in order, with titles" "d['data'][0]['first_slide']==1 and all(a['first_slide']<b['first_slide'] for a,b in zip(d['data'],d['data'][1:])) and all(c['title'] for c in d['data'])"
t "  not for AI-built courses" 409 "${A[@]}" -X POST $B/api/learn/materials/$PDF/suggest-units
t "  not for someone else's deck" 403 "${Bo[@]}" -X POST $B/api/learn/materials/$SM/suggest-units
t "a range past the last slide" 422 "${A[@]}" "${J[@]}" -X PUT -d "{\"units\":[{\"title\":\"x\",\"material_id\":$SM,\"first_page\":1,\"last_page\":9}]}" $B/api/learn/courses/$SC/slide-units
t "first after last" 422 "${A[@]}" "${J[@]}" -X PUT -d "{\"units\":[{\"title\":\"x\",\"material_id\":$SM,\"first_page\":3,\"last_page\":2}]}" $B/api/learn/courses/$SC/slide-units
t "a material of another course" 422 "${A[@]}" "${J[@]}" -X PUT -d "{\"units\":[{\"title\":\"x\",\"material_id\":$PDF,\"first_page\":1,\"last_page\":1}]}" $B/api/learn/courses/$SC/slide-units
t "kim cannot cut sam's slides" 403 "${Bo[@]}" "${J[@]}" -X PUT -d "{\"units\":[{\"title\":\"x\",\"material_id\":$SM,\"first_page\":1,\"last_page\":1}]}" $B/api/learn/courses/$SC/slide-units
t "cut into two units (by section)" 200 "${A[@]}" "${J[@]}" -X PUT -d "{\"units\":[{\"title\":\"Basics\",\"material_id\":$SM,\"first_page\":1,\"last_page\":3},{\"title\":\"  \",\"material_id\":$SM,\"first_page\":4,\"last_page\":8}]}" $B/api/learn/courses/$SC/slide-units
check "  two units with ranges" "[(u['title'],u['slides'][0]['first_page'],u['slides'][0]['last_page']) for u in d['data']]==[('Basics',1,3),('Slides 4–8',4,8)]"
U1=$(py "d['data'][0]['id']"); U2=$(py "d['data'][1]['id']")
t "the mode cannot change once there are units" 409 "${A[@]}" "${J[@]}" -X PUT -d '{"build_mode":"ai"}' $B/api/learn/courses/$SC/build-mode
t "creator reads unit 2's slides" 200 "${A[@]}" $B/api/learn/units/$U2/pages
check "  slides 4,5,6,8 (7 is hidden)" "[p['number'] for p in d['data'][0]['pages']]==[4,5,6,8]"
t "publish" 200 "${A[@]}" "${J[@]}" -d '{"visibility":"public","confirm_rights":true}' $B/api/learn/courses/$SC/publish
t "kim joins" 200 "${Bo[@]}" -X POST $B/api/learn/courses/$SC/join
t "learner outline" 200 "${Bo[@]}" $B/api/learn/courses/$SC
check "  units show their slide ranges" "d['data']['units'][0]['slides'][0]['last_page']==3"
check "  slide counts leave the hidden slide out" "[u['slides'][0]['count'] for u in d['data']['units']]==[3,4]"
t "kim reads unit 1" 200 "${Bo[@]}" $B/api/learn/units/$U1/pages
check "  slides 1-3, no speaker notes" "[p['number'] for p in d['data'][0]['pages']]==[1,2,3] and not any(p.get('notes') for p in d['data'][0]['pages'])"
check "  original file link for diagrams" "d['data'][0]['url'].startswith('http')"
t "unit 2 is locked" 403 "${Bo[@]}" $B/api/learn/units/$U2/pages
t "kim finishes unit 1" 200 "${Bo[@]}" -X POST $B/api/learn/units/$U1/complete
t "unit 2 opens" 200 "${Bo[@]}" $B/api/learn/units/$U2/pages
t "a stranger cannot read it" 404 "${C[@]}" $B/api/learn/units/$U2/pages
t "re-cut keeping unit 1 (progress stays)" 200 "${A[@]}" "${J[@]}" -X PUT -d "{\"units\":[{\"id\":$U1,\"title\":\"Basics\",\"material_id\":$SM,\"first_page\":1,\"last_page\":2},{\"id\":$U2,\"title\":\"Deeper\",\"material_id\":$SM,\"first_page\":3,\"last_page\":8}]}" $B/api/learn/courses/$SC/slide-units
t "unit 2 still open for kim" 200 "${Bo[@]}" $B/api/learn/units/$U2/pages
check "  now from slide 3" "d['data'][0]['pages'][0]['number']==3"

echo; echo "passed $pass, failed $failn"
[ "$failn" = 0 ]
