#!/usr/bin/env bash
# End-to-end test of the course builder (FEATURES.md step 1) against a running
# API with an EMPTY database, storage configured, and AI_PROVIDER=fake.
#   BASE=http://localhost:18091 scripts/smoke-learn.sh
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
A=(-b "$D/a" -c "$D/a"); Bo=(-b "$D/b" -c "$D/b"); C=(-b "$D/c" -c "$D/c")

# Test files: a tiny PDF, a .pptx with two slides, a text file, an old .ppt
printf '%%PDF-1.4\n1 0 obj<</Type/Catalog>>endobj\ntrailer<</Root 1 0 R>>\n%%%%EOF\n' > "$D/notes.pdf"
python3 - "$D" <<'PY'
import sys, zipfile
d = sys.argv[1]
s = '<p:sld xmlns:p="p" xmlns:a="a"><a:p><a:r><a:t>%s</a:t></a:r></a:p></p:sld>'
with zipfile.ZipFile(f"{d}/slides.pptx", "w") as z:
    z.writestr("ppt/slides/slide1.xml", s % "Goroutines")
    z.writestr("ppt/slides/slide2.xml", s % "Channels")
open(f"{d}/readme.txt", "w").write("plain text")
open(f"{d}/old.ppt", "wb").write(b"\xd0\xcf\x11\xe0 old office")
PY

for u in a:alice b:bob c:carol; do
  jar=${u%%:*}; name=${u#*:}
  t "register $name" 201 -b "$D/$jar" -c "$D/$jar" "${J[@]}" -d "{\"name\":\"$name\",\"email\":\"$name@test.io\",\"password\":\"secret123\",\"password_confirmation\":\"secret123\"}" $B/register
done

t "alice creates a course" 201 "${A[@]}" -F course_title="Go Concurrency" -F course_description="Learn goroutines" -F course_category_id=1 $B/api/course
CID=$(py "d['course_id']")
t "builder view (creator)" 200 "${A[@]}" $B/api/learn/courses/$CID
check "  course starts as draft" "d['data']['course']['course_status']=='draft' and d['data']['role']=='creator'"

t "upload PDF" 201 "${A[@]}" -F "file=@$D/notes.pdf" -F title="Lecture notes" $B/api/learn/courses/$CID/materials
t "upload .pptx" 201 "${A[@]}" -F "file=@$D/slides.pptx" $B/api/learn/courses/$CID/materials
t "add YouTube video" 201 "${A[@]}" "${J[@]}" -d '{"kind":"video","url":"https://youtu.be/dQw4w9WgXcQ","title":"Intro video"}' $B/api/learn/courses/$CID/materials
check "  video id parsed, being watched by the video AI" "d['data']['youtube_id']=='dQw4w9WgXcQ' and d['data']['status'] in ('processing','ready')"
t "add web link" 201 "${A[@]}" "${J[@]}" -d '{"kind":"link","url":"https://go.dev/tour","title":"Go tour","note":"Official interactive tour"}' $B/api/learn/courses/$CID/materials
t "categories" 200 "${A[@]}" $B/api/learn/categories
check "  8 seeded categories" "len(d['data'])==8"
t "get a direct-upload token" 200 "${A[@]}" -X POST $B/api/learn/courses/$CID/upload-url
UPURL=$(py "d['data']['url']"); UPTOK=$(py "d['data']['token']")
t "bob cannot get an upload token" 403 "${Bo[@]}" -X POST $B/api/learn/courses/$CID/upload-url
t "browser pre-check from the frontend site" 204 -X OPTIONS -H "Origin: ${ORIGIN:-http://localhost:3000}" -H "Access-Control-Request-Method: POST" -D "$D/h" "$UPURL"
grep -qi "access-control-allow-origin: ${ORIGIN:-http://localhost:3000}" "$D/h" && { pass=$((pass+1)); echo "ok     CORS allows the frontend site"; } || { failn=$((failn+1)); echo "FAIL   CORS header missing"; }
curl -s -o /dev/null -X OPTIONS -H "Origin: https://evil.example" -H "Access-Control-Request-Method: POST" -D "$D/h2" "$UPURL"
grep -qi "access-control-allow-origin" "$D/h2" && { failn=$((failn+1)); echo "FAIL   CORS must not allow other sites"; } || { pass=$((pass+1)); echo "ok     CORS refuses other sites"; }
t "direct upload with a bad token" 401 -H "X-Upload-Token: 2.9999999999.deadbeef" -F "file=@$D/notes.pdf" "$UPURL"
t "direct upload (token, no cookie)" 201 -H "X-Upload-Token: $UPTOK" -F "file=@$D/notes.pdf" -F title="Direct upload" "$UPURL"
DIRECT=$(py "d['data']['id']")
t "token is bound to its course" 401 -H "X-Upload-Token: $UPTOK" -F "file=@$D/notes.pdf" "${UPURL%/*}/999"
t "remove the direct-upload test file" 200 "${A[@]}" -X DELETE $B/api/learn/materials/$DIRECT
t "reject .txt file" 422 "${A[@]}" -F "file=@$D/readme.txt" $B/api/learn/courses/$CID/materials
t "reject old .ppt with a clear reason" 422 "${A[@]}" -F "file=@$D/old.ppt" $B/api/learn/courses/$CID/materials
check "  message says save as .pptx" "'.pptx' in d['message']"
t "reject non-YouTube video" 422 "${A[@]}" "${J[@]}" -d '{"kind":"video","url":"https://vimeo.com/1","title":"x"}' $B/api/learn/courses/$CID/materials
t "reject javascript: link" 422 "${A[@]}" "${J[@]}" -d '{"kind":"link","url":"javascript:alert(1)","title":"x"}' $B/api/learn/courses/$CID/materials
t "bob cannot add materials" 403 "${Bo[@]}" "${J[@]}" -d '{"kind":"link","url":"https://x.io","title":"x"}' $B/api/learn/courses/$CID/materials
t "bob cannot see the draft" 404 "${Bo[@]}" $B/api/learn/courses/$CID

for i in $(seq 1 20); do
  curl -s "${A[@]}" -o "$D/out" $B/api/learn/courses/$CID/materials
  python3 -c "import json,sys; d=json.load(open('$D/out')); sys.exit(0 if all(m['status']!='processing' for m in d['data']) else 1)" && break; sleep 1
done
check "all 4 materials ready with digests" "len(d['data'])==4 and all(m['status']=='ready' for m in d['data']) and all(m['digest']['summary'] for m in d['data'])"

t "organise" 202 "${A[@]}" -X POST $B/api/learn/courses/$CID/organise
for i in $(seq 1 20); do
  curl -s "${A[@]}" -o "$D/out" $B/api/learn/courses/$CID
  [ "$(py "d['data']['course']['organise_status']")" != "processing" ] && break; sleep 1
done
check "organised into units with materials" "d['data']['course']['organise_status']=='ready' and len(d['data']['units'])==4 and all(len(u['materials'])==1 for u in d['data']['units'])"
check "  every unit has concepts with sources" "all(len(u['concepts'])>=1 and all(len(c['sources'])>=1 for c in u['concepts']) for u in d['data']['units'])"
check "  PDF concept links to a page" "any(src['url'].endswith('#page=1') and src['hint']=='pages 1–2' for u in d['data']['units'] for c in u['concepts'] for src in c['sources'] if src['kind']=='pdf')"
check "  video concept jumps to its timestamp" "any(src['url'].endswith('&t=30s') and src['hint']=='from 0:30' for u in d['data']['units'] for c in u['concepts'] for src in c['sources'] if src['kind']=='video')"
check "  slide concept shows slide number" "any(src['hint']=='slide 1' for u in d['data']['units'] for c in u['concepts'] for src in c['sources'] if src['kind']=='slides')"
UNIT_A=$(py "d['data']['units'][0]['id']"); CON1=$(py "d['data']['units'][0]['concepts'][0]['id']"); CON2=$(py "d['data']['units'][0]['concepts'][1]['id']")
t "edit a concept" 200 "${A[@]}" -X PUT "${J[@]}" -d '{"title":"Goroutines","explanation":"A **goroutine** is a lightweight thread."}' $B/api/learn/concepts/$CON1
check "  edited, sources kept" "d['data'][0]['title']=='Goroutines' and len(d['data'][0]['sources'])>=1"
t "bob cannot edit a concept" 403 "${Bo[@]}" -X PUT "${J[@]}" -d '{"title":"x"}' $B/api/learn/concepts/$CON1
t "reorder concepts" 200 "${A[@]}" -X PUT "${J[@]}" -d "{\"concept_ids\":[$CON2,$CON1]}" $B/api/learn/units/$UNIT_A/concepts/order
check "  new order" "[c['id'] for c in d['data']]==[$CON2,$CON1]"
t "add a concept by hand" 201 "${A[@]}" "${J[@]}" -d '{"title":"Channels","explanation":"Typed pipes."}' $B/api/learn/units/$UNIT_A/concepts
CON3=$(py "d['data']['id']")
t "delete that concept" 200 "${A[@]}" -X DELETE $B/api/learn/concepts/$CON3
curl -s "${A[@]}" -o "$D/out" $B/api/learn/courses/$CID
U1=$(py "d['data']['units'][0]['id']"); U2=$(py "d['data']['units'][1]['id']"); U3=$(py "d['data']['units'][2]['id']"); U4=$(py "d['data']['units'][3]['id']")
M1=$(py "d['data']['materials'][0]['id']"); M2=$(py "d['data']['materials'][1]['id']")

t "edit unit text and materials" 200 "${A[@]}" -X PUT "${J[@]}" -d "{\"title\":\"Foundations\",\"body\":\"# Start here\",\"material_ids\":[$M1,$M2,99999]}" $B/api/learn/units/$U1
check "  foreign material id ignored" "d['data']['title']=='Foundations' and [m['id'] for m in d['data']['materials']]==[$M1,$M2]"
t "add a manual unit" 201 "${A[@]}" "${J[@]}" -d '{"title":"Wrap-up","body":"Summary"}' $B/api/learn/courses/$CID/units
U5=$(py "d['data']['id']")
t "reorder units (wrap-up moved to 2nd)" 200 "${A[@]}" -X PUT "${J[@]}" -d "{\"unit_ids\":[$U1,$U5,$U2,$U3,$U4]}" $B/api/learn/courses/$CID/units/order
t "reorder with a missing unit fails" 422 "${A[@]}" -X PUT "${J[@]}" -d "{\"unit_ids\":[$U1,$U2]}" $B/api/learn/courses/$CID/units/order
t "delete the manual unit" 200 "${A[@]}" -X DELETE $B/api/learn/units/$U5
t "bob cannot edit units" 403 "${Bo[@]}" -X PUT "${J[@]}" -d '{"title":"hacked"}' $B/api/learn/units/$U1

t "publish public without rights confirmation" 422 "${A[@]}" "${J[@]}" -d '{"visibility":"public"}' $B/api/learn/courses/$CID/publish
t "publish public" 200 "${A[@]}" "${J[@]}" -d '{"visibility":"public","confirm_rights":true}' $B/api/learn/courses/$CID/publish
t "organise refused once published" 422 "${A[@]}" -X POST $B/api/learn/courses/$CID/organise

t "bob browses" 200 "${Bo[@]}" $B/api/learn/browse
check "  course listed with 4 units" "any(c['course_id']==$CID and c['units']==4 for c in d['data'])"
t "bob sees outline before joining" 200 "${Bo[@]}" $B/api/learn/courses/$CID
check "  all locked, no unit text" "d['data']['enrolled']==False and all(u['locked'] for u in d['data']['units']) and all(u['body']=='' for u in d['data']['units'])"
t "bob cannot open a unit before joining" 404 "${Bo[@]}" $B/api/learn/units/$U1
t "bob joins" 200 "${Bo[@]}" -X POST $B/api/learn/courses/$CID/join
t "learner view after joining" 200 "${Bo[@]}" $B/api/learn/courses/$CID
check "  only unit 1 open" "[u['locked'] for u in d['data']['units']]==[False,True,True,True]"
check "  outline lists concept names, no explanations" "len(d['data']['units'][1]['concept_names'])>=1 and not d['data']['units'][1].get('concepts')"
t "unit 1 opens" 200 "${Bo[@]}" $B/api/learn/units/$U1
check "  with its text and materials" "d['data']['body']=='# Start here' and len(d['data']['materials'])==2"
check "  with concepts and their sources" "any(c['title']=='Goroutines' and len(c['sources'])>=1 for c in d['data']['concepts'])"
t "unit 2 is locked" 403 "${Bo[@]}" $B/api/learn/units/$U2
t "cannot finish unit 2 first" 403 "${Bo[@]}" -X POST $B/api/learn/units/$U2/complete
t "finish unit 1" 200 "${Bo[@]}" -X POST $B/api/learn/units/$U1/complete
t "unit 2 now opens" 200 "${Bo[@]}" $B/api/learn/units/$U2
t "unit 3 still locked" 403 "${Bo[@]}" $B/api/learn/units/$U3
t "finish unit 1 again (idempotent)" 200 "${Bo[@]}" -X POST $B/api/learn/units/$U1/complete
for u in $U2 $U3 $U4; do t "finish unit $u" 200 "${Bo[@]}" -X POST $B/api/learn/units/$u/complete; done
check "  course completed" "d['course_completed']==True and d['completed_units']==4"
t "bob's progress on /me" 200 "${Bo[@]}" $B/api/learn/me
check "  joined course shows 4/4" "d['data']['joined'][0]['completed_units']==4 and d['data']['joined'][0]['total_units']==4"
t "carol (not joined) cannot open unit 1" 404 "${C[@]}" $B/api/learn/units/$U1
t "alice cannot join her own course" 422 "${A[@]}" -X POST $B/api/learn/courses/$CID/join

t "unpublish" 200 "${A[@]}" -X POST $B/api/learn/courses/$CID/unpublish
t "browse no longer lists it" 200 "${Bo[@]}" $B/api/learn/browse
check "  not listed" "not any(c['course_id']==$CID for c in d['data'])"
t "delete a material" 200 "${A[@]}" -X DELETE $B/api/learn/materials/$M2
t "creator view after delete" 200 "${A[@]}" $B/api/learn/courses/$CID
check "  removed from its unit" "[m['id'] for m in d['data']['units'][0]['materials']]==[$M1]"

t "alice creates a 2nd course (free limit 2)" 201 "${A[@]}" -F course_title="Second" -F course_category_id=1 $B/api/course
t "3rd course blocked by the free limit" 403 "${A[@]}" -F course_title="Third" -F course_category_id=1 $B/api/course
t "/me reports the limit" 200 "${A[@]}" $B/api/learn/me
check "  can_create_course is false" "d['data']['can_create_course']==False and len(d['data']['created'])==2"

echo "passed $pass, failed $failn"
[ "$failn" = 0 ]
