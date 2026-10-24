#!/usr/bin/env bash
# End-to-end test of unit tests (FEATURES.md step 3: F2.1 to F2.4) against a
# running API with AI_PROVIDER=fake. Works on any database (unique emails).
#   BASE=http://localhost:18091 scripts/smoke-tests.sh
# Optional: PSQL="docker exec -i ff-db psql -U postgres -d focusflow" to also
# test the auto-submit at time-out (moves a deadline into the past).
set -u
B=${BASE:-http://localhost:8091}
D=$(mktemp -d); trap 'rm -rf "$D"' EXIT
RUN=$(date +%s)$RANDOM
pass=0; failn=0
t() {
  local name=$1 want=$2; shift 2
  local got; got=$(curl -s -o "$D/out" -w "%{http_code}" "$@")
  if [ "$got" = "$want" ]; then pass=$((pass+1)); printf "ok   %-56s %s\n" "$name" "$got"
  else failn=$((failn+1)); printf "FAIL %-56s got %s want %s: %s\n" "$name" "$got" "$want" "$(head -c 300 "$D/out")"; fi
}
py() { python3 -c "import json,sys; d=json.load(open('$D/out')); print($1)"; }
check() { if python3 -c "import json,sys; d=json.load(open('$D/out')); sys.exit(0 if ($2) else 1)"; then pass=$((pass+1)); printf "ok   %s\n" "$1"; else failn=$((failn+1)); printf "FAIL %s: %s\n" "$1" "$(head -c 400 "$D/out")"; fi; }
# wait_for "<url>" "<python condition on d>" (creator cookie)
wait_for() { for _ in $(seq 1 30); do curl -s "${T[@]}" -o "$D/out" "$1"; python3 -c "import json,sys; d=json.load(open('$D/out')); sys.exit(0 if ($2) else 1)" 2>/dev/null && return 0; sleep 1; done; return 1; }
J=(-H "Content-Type: application/json")
T=(-b "$D/t" -c "$D/t"); L=(-b "$D/l" -c "$D/l"); L2=(-b "$D/l2" -c "$D/l2"); O=(-b "$D/o" -c "$D/o")

for u in t:teacher l:learner l2:second o:outsider; do
  jar=${u%%:*}; name=${u#*:}
  t "register $name" 201 -b "$D/$jar" -c "$D/$jar" "${J[@]}" -d "{\"name\":\"$name\",\"email\":\"$name.$RUN@test.io\",\"password\":\"secret123\",\"password_confirmation\":\"secret123\"}" $B/register
done

# A course with two units (fake organiser: one unit per material)
t "teacher creates a course" 201 "${T[@]}" -F course_title="Tests $RUN" -F course_description="Unit tests" -F course_category_id=1 $B/api/course
CID=$(py "d['course_id']")
t "add link 1" 201 "${T[@]}" "${J[@]}" -d '{"kind":"link","url":"https://go.dev/tour","title":"Go tour"}' $B/api/learn/courses/$CID/materials
t "add link 2" 201 "${T[@]}" "${J[@]}" -d '{"kind":"link","url":"https://go.dev/doc","title":"Go docs"}' $B/api/learn/courses/$CID/materials
t "organise" 202 "${T[@]}" -X POST $B/api/learn/courses/$CID/organise
wait_for $B/api/learn/courses/$CID "d['data']['course']['organise_status']=='ready'" || echo "FAIL organise did not finish"
U1=$(py "d['data']['units'][0]['id']"); U2=$(py "d['data']['units'][1]['id']")

# ── creator: pool and settings ──
t "creator opens the unit test" 200 "${T[@]}" $B/api/learn/units/$U1/test
check "  defaults, empty pool, AI available" "d['data']['role']=='creator' and d['data']['questions']==[] and d['data']['settings']['pass_mark']==60 and d['data']['ai_available']"
t "cannot turn the test on without questions" 422 "${T[@]}" -X PUT "${J[@]}" -d '{"enabled":true}' $B/api/learn/units/$U1/test
t "learner cannot write questions" 403 "${L[@]}" -X POST $B/api/learn/units/$U1/questions/generate
t "AI writes the pool" 202 "${T[@]}" -X POST $B/api/learn/units/$U1/questions/generate
wait_for $B/api/learn/units/$U1/test "d['data']['settings']['pool_status']=='ready'" || echo "FAIL pool not ready"
check "  pool written (mcq + short)" "len(d['data']['questions'])==3 and {q['kind'] for q in d['data']['questions']}=={'mcq','short'}"
Q1=$(py "[q['id'] for q in d['data']['questions'] if q['kind']=='mcq'][0]")
t "mcq with one option refused" 422 "${T[@]}" "${J[@]}" -d '{"kind":"mcq","prompt":"x?","options":["a"],"correct":0}' $B/api/learn/units/$U1/questions
t "mcq with the correct index out of range refused" 422 "${T[@]}" "${J[@]}" -d '{"kind":"mcq","prompt":"x?","options":["a","b"],"correct":2}' $B/api/learn/units/$U1/questions
t "written question without a guide refused" 422 "${T[@]}" "${J[@]}" -d '{"kind":"explain","prompt":"Why?"}' $B/api/learn/units/$U1/questions
t "add a question by hand" 201 "${T[@]}" "${J[@]}" -d '{"kind":"mcq","prompt":"Manual?","options":["Right answer","Nope"],"correct":0}' $B/api/learn/units/$U1/questions
check "  marked manual" "d['data']['source']=='manual'"
t "edit an AI question" 200 "${T[@]}" -X PUT "${J[@]}" -d '{"prompt":"Edited question?"}' $B/api/learn/questions/$Q1
check "  becomes 'edited'" "d['data']['source']=='edited' and d['data']['prompt']=='Edited question?'"
t "learner cannot edit questions" 403 "${L[@]}" -X PUT "${J[@]}" -d '{"prompt":"hack"}' $B/api/learn/questions/$Q1
t "AI writes the pool again" 202 "${T[@]}" -X POST $B/api/learn/units/$U1/questions/generate
wait_for $B/api/learn/units/$U1/test "d['data']['settings']['pool_status']=='ready'" || echo "FAIL pool not ready"
check "  edited and manual questions kept, AI ones replaced" "len(d['data']['questions'])==5 and sum(q['source']=='edited' for q in d['data']['questions'])==1 and sum(q['source']=='manual' for q in d['data']['questions'])==1"
t "bad settings refused" 422 "${T[@]}" -X PUT "${J[@]}" -d '{"pass_mark":0}' $B/api/learn/units/$U1/test
t "settings: 3 questions, 2 attempts, 10 min, test on" 200 "${T[@]}" -X PUT "${J[@]}" -d '{"enabled":true,"questions_per_test":3,"max_attempts":2,"time_limit_minutes":10,"pass_mark":60}' $B/api/learn/units/$U1/test
check "  saved" "d['data']['enabled'] and d['data']['questions_per_test']==3 and d['data']['track_tab'] and d['data']['track_paste']"
# Unit 2: a manual pool (the free plan counts AI runs), teacher review on
for i in 1 2; do t "unit 2 question $i" 201 "${T[@]}" "${J[@]}" -d "{\"kind\":\"mcq\",\"prompt\":\"U2 q$i?\",\"options\":[\"Right answer\",\"No\",\"Never\"],\"correct\":0}" $B/api/learn/units/$U2/questions; done
t "unit 2 written question" 201 "${T[@]}" "${J[@]}" -d '{"kind":"short","prompt":"Say it","answer_guide":"- says correct"}' $B/api/learn/units/$U2/questions
t "unit 2 test on, teacher review" 200 "${T[@]}" -X PUT "${J[@]}" -d '{"enabled":true,"questions_per_test":3,"teacher_review":true,"time_limit_minutes":0}' $B/api/learn/units/$U2/test
t "builder view shows the tests" 200 "${T[@]}" $B/api/learn/courses/$CID
check "  unit summaries" "d['data']['units'][0]['test']['enabled'] and d['data']['units'][0]['test']['questions']==5"

t "publish" 200 "${T[@]}" "${J[@]}" -d '{"visibility":"public","confirm_rights":true}' $B/api/learn/courses/$CID/publish
t "learner joins" 200 "${L[@]}" -X POST $B/api/learn/courses/$CID/join
t "second learner joins" 200 "${L2[@]}" -X POST $B/api/learn/courses/$CID/join
t "learner course view" 200 "${L[@]}" $B/api/learn/courses/$CID
check "  unit 1 has a test, unit 2 locked" "d['data']['units'][0]['test']['enabled'] and d['data']['units'][1]['locked']"
t "marking done by hand is refused" 403 "${L[@]}" -X POST $B/api/learn/units/$U1/complete
t "unit 2 test is locked" 403 "${L[@]}" $B/api/learn/units/$U2/test
t "outsider cannot see the test" 404 "${O[@]}" $B/api/learn/units/$U1/test
t "creator cannot start a test" 422 "${T[@]}" -X POST $B/api/learn/units/$U1/test/start

# ── learner: attempt 1 (fails) ──
t "learner test page" 200 "${L[@]}" $B/api/learn/units/$U1/test
check "  settings shown before starting" "d['data']['can_start'] and d['data']['settings']=={'questions':3,'pass_mark':60,'time_limit_minutes':10,'max_attempts':2,'teacher_review':False,'track_tab':True,'track_paste':True}"
t "start attempt 1" 200 "${L[@]}" -X POST $B/api/learn/units/$U1/test/start
A1=$(py "d['data']['id']")
check "  3 questions with a deadline" "len(d['data']['items'])==3 and d['data']['deadline'] and d['data']['status']=='in_progress'"
grep -q '"correct"\|answer_guide\|ai_mark' "$D/out" && { failn=$((failn+1)); echo "FAIL   learner sees answers"; } || { pass=$((pass+1)); echo "ok     no correct answers or guides sent to the learner"; }
t "start again resumes the same attempt" 200 "${L[@]}" -X POST $B/api/learn/units/$U1/test/start
check "  same id" "d['data']['id']==$A1"
t "log browser signals" 204 "${L[@]}" "${J[@]}" -d '{"events":["tab_hidden","paste","made_up"]}' $B/api/learn/attempts/$A1/events
t "another learner cannot touch it" 404 "${L2[@]}" "${J[@]}" -X PUT -d '{"answers":[]}' $B/api/learn/attempts/$A1/answers
t "creator cannot answer it" 403 "${T[@]}" "${J[@]}" -X PUT -d '{"answers":[]}' $B/api/learn/attempts/$A1/answers
t "creator sees the attempt with answers" 200 "${T[@]}" $B/api/learn/attempts/$A1
check "  with evidence (unknown signal dropped)" "d['data']['evidence']=={'tab_hidden':1,'paste':1} and d['data']['learner']['name']=='learner'"
# Wrong answers on purpose: every mcq gets an option that is not the right one
WRONG=$(py "json.dumps([{'index':i,'choice':(it['correct']+1)%len(it['options'])} if it['kind']=='mcq' else {'index':i,'answer':'no idea'} for i,it in enumerate(d['data']['attempt']['items'])])")
RIGHT=$(py "json.dumps([{'index':i,'choice':it['correct']} if it['kind']=='mcq' else {'index':i,'answer':'this is correct'} for i,it in enumerate(d['data']['attempt']['items'])])")
t "autosave answers" 200 "${L[@]}" -X PUT "${J[@]}" -d "{\"answers\":$WRONG}" $B/api/learn/attempts/$A1/answers
t "submit attempt 1" 200 "${L[@]}" -X POST "${J[@]}" -d "{\"answers\":$WRONG}" $B/api/learn/attempts/$A1/submit
curl -s "${L[@]}" -o "$D/out" $B/api/learn/attempts/$A1
for _ in $(seq 1 15); do [ "$(py "d['data']['status']")" = done ] && break; sleep 1; curl -s "${L[@]}" -o "$D/out" $B/api/learn/attempts/$A1; done
check "  failed with 0%" "d['data']['status']=='done' and d['data']['score_percent']==0 and not d['data']['passed']"
check "  result shows marks and feedback, still no answers" "all(it['mark']==0 for it in d['data']['items']) and 'correct' not in d['data']['items'][0]"
t "submit twice refused" 409 "${L[@]}" -X POST "${J[@]}" -d '{"answers":[]}' $B/api/learn/attempts/$A1/submit
t "unit 2 still locked" 403 "${L[@]}" $B/api/learn/units/$U2

# ── attempt 2 (passes; written answer marked by the AI) ──
t "start attempt 2" 200 "${L[@]}" -X POST $B/api/learn/units/$U1/test/start
A2=$(py "d['data']['id']")
check "  a new attempt, number 2" "d['data']['id']!=$A1 and d['data']['number']==2"
curl -s "${T[@]}" -o "$D/out" $B/api/learn/attempts/$A2
RIGHT=$(py "json.dumps([{'index':i,'choice':it['correct']} if it['kind']=='mcq' else {'index':i,'answer':'this is correct'} for i,it in enumerate(d['data']['attempt']['items'])])")
t "submit attempt 2" 200 "${L[@]}" -X POST "${J[@]}" -d "{\"answers\":$RIGHT}" $B/api/learn/attempts/$A2/submit
for _ in $(seq 1 15); do curl -s "${L[@]}" -o "$D/out" $B/api/learn/attempts/$A2; [ "$(py "d['data']['status']")" = done ] && break; sleep 1; done
check "  passed with 100%" "d['data']['status']=='done' and d['data']['score_percent']==100 and d['data']['passed']"
t "unit 2 now opens" 200 "${L[@]}" $B/api/learn/units/$U2
t "test page after passing" 200 "${L[@]}" $B/api/learn/units/$U1/test
check "  no more attempts, 2 listed" "not d['data']['can_start'] and d['data']['reason']=='You passed this test.' and len(d['data']['attempts'])==2"
t "a 3rd attempt is refused" 422 "${L[@]}" -X POST $B/api/learn/units/$U1/test/start

# ── teacher review (unit 2) ──
t "start unit 2 test" 200 "${L[@]}" -X POST $B/api/learn/units/$U2/test/start
A3=$(py "d['data']['id']")
curl -s "${T[@]}" -o "$D/out" $B/api/learn/attempts/$A3
RIGHT=$(py "json.dumps([{'index':i,'choice':it['correct']} if it['kind']=='mcq' else {'index':i,'answer':'this is correct'} for i,it in enumerate(d['data']['attempt']['items'])])")
WIDX=$(py "[i for i,it in enumerate(d['data']['attempt']['items']) if it['kind']=='short'][0]")
t "submit unit 2 test" 200 "${L[@]}" -X POST "${J[@]}" -d "{\"answers\":$RIGHT}" $B/api/learn/attempts/$A3/submit
for _ in $(seq 1 15); do curl -s "${L[@]}" -o "$D/out" $B/api/learn/attempts/$A3; [ "$(py "d['data']['status']")" = review ] && break; sleep 1; done
check "  waits for the teacher, no score shown" "d['data']['status']=='review' and d['data']['score_percent'] is None and all(it.get('mark') is None for it in d['data']['items'])"
t "teacher's review list" 200 "${T[@]}" $B/api/learn/courses/$CID/attempts
check "  1 waiting" "len(d['data'])==1 and d['data'][0]['id']==$A3 and d['data'][0]['learner_name']=='learner'"
t "learner cannot see the review list" 403 "${L[@]}" $B/api/learn/courses/$CID/attempts
t "learner cannot review" 403 "${L[@]}" "${J[@]}" -d '{"marks":[]}' $B/api/learn/attempts/$A3/review
t "bad mark refused" 422 "${T[@]}" "${J[@]}" -d "{\"marks\":[{\"index\":$WIDX,\"mark\":0.3}]}" $B/api/learn/attempts/$A3/review
t "teacher lowers the written answer to half" 200 "${T[@]}" "${J[@]}" -d "{\"marks\":[{\"index\":$WIDX,\"mark\":0.5,\"note\":\"Explain why, not only what.\"}]}" $B/api/learn/attempts/$A3/review
check "  83.3%, passed, reviewer recorded" "d['data']['status']=='done' and d['data']['score_percent']==83.3 and d['data']['passed'] and d['data']['reviewed_by']"
t "creator sees the change log" 200 "${T[@]}" $B/api/learn/attempts/$A3
check "  one change: 1 -> 0.5 with the note" "len(d['data']['changes'])==1 and d['data']['changes'][0]['old_mark']==1 and d['data']['changes'][0]['new_mark']==0.5"
t "review twice refused" 422 "${T[@]}" "${J[@]}" -d '{"marks":[]}' $B/api/learn/attempts/$A3/review
t "learner gets the result" 200 "${L[@]}" $B/api/learn/attempts/$A3
check "  with the teacher's note" "d['data']['score_percent']==83.3 and d['data']['items'][$WIDX]['teacher_note']=='Explain why, not only what.'"
t "course finished" 200 "${L[@]}" $B/api/learn/me
check "  2/2 units" "[j for j in d['data']['joined'] if j['course']['course_id']==$CID][0]['completed_units']==2"

# ── attempt limit ──
for n in 1 2; do
  t "second learner attempt $n" 200 "${L2[@]}" -X POST $B/api/learn/units/$U1/test/start
  AX=$(py "d['data']['id']")
  t "  submits empty" 200 "${L2[@]}" -X POST "${J[@]}" -d '{"answers":[]}' $B/api/learn/attempts/$AX/submit
done
t "attempts used up" 422 "${L2[@]}" -X POST $B/api/learn/units/$U1/test/start
check "  says why" "'all 2 attempts' in d['message']"

# ── time-out (needs PSQL) ──
if [ -n "${PSQL:-}" ]; then
  t "raise the attempt limit" 200 "${T[@]}" -X PUT "${J[@]}" -d '{"max_attempts":0}' $B/api/learn/units/$U1/test
  t "second learner attempt 3" 200 "${L2[@]}" -X POST $B/api/learn/units/$U1/test/start
  AX=$(py "d['data']['id']")
  t "  saves a right mcq answer" 200 "${L2[@]}" -X PUT "${J[@]}" -d '{"answers":[]}' $B/api/learn/attempts/$AX/answers
  echo "UPDATE test_attempts SET deadline = now() - interval '2 minutes' WHERE id = $AX;" | $PSQL >/dev/null
  t "late answers refused" 409 "${L2[@]}" -X PUT "${J[@]}" -d '{"answers":[]}' $B/api/learn/attempts/$AX/answers
  t "opening it after the deadline" 200 "${L2[@]}" $B/api/learn/attempts/$AX
  check "  auto-submitted" "d['data']['auto_submitted'] and d['data']['status']=='done'"
fi

echo "passed $pass, failed $failn"
[ "$failn" = 0 ]
