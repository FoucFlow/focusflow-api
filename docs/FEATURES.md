# FocusFlow: Feature Specification

Version 0.3 · 26 October 2026 · replaces v0.2 (24 October 2026)

Feature IDs are stable: use them in commits, issues and pull requests.

**What changed in v0.3:** plans (Personal, Pro, Institution); a second way to
build a course, **slides as written**, where learners read the teacher's own
slides page by page and the AI only structures and adds references (M3); one
quiz feature for practice, unit tests and exams, with restriction levels the
creator picks and three ways to make questions (F2.1–F2.3, F2.8–F2.12); invite
links and institutions (M4); a new build order (section 6).

## 1. What FocusFlow is

Anyone can **create a course** from their own materials or **take a course**
someone else made. Self-taught learners, teachers and university lecturers use
the same app.

There are two ways to build a course, chosen when it is created:

| Build mode | For | What learners read | What the AI does |
| --- | --- | --- | --- |
| **Slides as written** (default) | Teachers with an existing PowerPoint or PDF course | The teacher's slides, one page at a time, exactly as written | Suggests units and titles, finds outside references. Never changes the teaching. |
| **AI-written lessons** | Self-learners turning videos and notes into a course | Unit texts the AI writes from the materials | Reads, organises and writes the lessons; the creator edits and approves |

Each unit can end with a **unit test** and the course with a **final exam**.
Learners finish a unit before the next one opens.

## 2. Plans and roles

| Plan | Who | Gets |
| --- | --- | --- |
| **Personal** (free) | Anyone | Create and publish courses within the free limits (section 7); learn for free |
| **Pro** (later) | Creators who want more | More courses and materials, more AI features and allowance |
| **Institution** (later) | A university or school | Lecturers create courses for their classes, invite students, give quizzes and exams, and see how each student is doing |

| Role | Who | Can do |
| --- | --- | --- |
| Creator | Any signed-in user, for courses they made | Build, organise and publish the course; write or import quizzes; set test rules; review marks |
| Learner | Any signed-in user who joined a course | Study units in order, take tests, see their own progress |
| Lecturer (later) | A creator inside an institution | Everything a creator does, plus invite a class and see class results |
| Institution admin (later) | Staff of the institution | Add lecturers, approve data access, manage consent records |
| Admin | Platform staff | Manage users, handle reported courses, see aggregate stats |

A user can be a creator of some courses and a learner in others.

## 3. Principles

- The creator is in control: AI drafts, the creator decides. Nothing AI-made reaches learners until approved.
- **In "slides as written" courses the AI never rewrites, summarises or adds to the teaching text.** It only suggests structure and references.
- AI gives explanations and hints, not test answers.
- The AI never decides that someone cheated. It flags evidence; a person decides.
- Learners see a test's rules (timer, tracking, camera) before they start.
- Send the minimum to the AI model; never send a learner's name.
- Everything is exportable in open formats.

## 4. Features

**Step** column: **Done** = live; **A**, **B**, **C** = the next steps, in
order (section 6); **Later** = after step C, not scheduled.

### M1. Course section

| ID | Feature | Step | Acceptance criteria |
| --- | --- | --- | --- |
| F1.1 | Create course | Done | Title, description, category, language, visibility (private by default). The creator owns it. |
| F1.2 | Course canvas: materials | Done | Upload PDF (≤ 25 MB) and PowerPoint .pptx (≤ 25 MB); add video links (YouTube) and web links. Types and sizes are validated; a failed upload says why. Each material has a title and type. |
| F1.3 | Material digest | Done | After upload, each material is read in the background and a digest of concepts with their sources (page, slide, timestamp) is stored. Status: processing, ready or failed (with reason). |
| F1.4 | AI organise into units | Done | AI-written lessons mode: the AI proposes units with written concept explanations; the creator edits, reorders, adds or deletes, or runs again. |
| F1.5 | Units | Done | Units have an order, title, text (Markdown) and linked materials. Learners only see published courses. |
| F1.6 | Publish and browse | Done | The creator publishes; public courses appear in Discover. Before going public the creator confirms they may share the uploaded files. |
| F1.7 | Learning flow | Done | A learner joins a course and opens units in order. Unit 1 is open; each next unit opens when the previous one is finished. Until tests are live, "finished" = the learner marks the unit done. |
| F1.8 | AI-suggested references | Done | Per unit, the AI finds outside videos and links with source and why; links are verified. The creator approves or rejects each. |
| F1.9 | Report a course | Later | Any user can report a public course; admins review. |
| F1.10 | Build mode | A | Chosen when creating a course: "Use my slides as written" (default) or "Let AI write lessons". Can be changed until the course has units. |

### M2. Quizzes, tests and exams

**One quiz feature for everything.** A practice quiz, a unit test and a final
exam are the same thing with different settings. The creator decides what the
quiz covers, where it sits, how it is made and how strict it is. The engine
(F2.1–F2.4) is built in the API; the screens are step B.

| ID | Feature | Step | Acceptance criteria |
| --- | --- | --- | --- |
| F2.1 | Make a quiz three ways | B | Every creator can build a quiz by **writing** questions (F2.10), **importing** them (F2.9) or asking the **AI to create** them from their material: a unit, chosen materials or slide ranges, or the whole course. Ways can be mixed in one quiz. AI questions are drafts the creator edits, keeps or removes; in slides-as-written courses they come only from the teacher's own slides. API done for writing and AI (per unit). |
| F2.2 | Place a quiz anywhere | B | A quiz can sit at the end of a unit, between units, or at the end of the course (final exam, F2.6), and covers the units the creator picks. Each quiz is either **required** (must pass to go on; pass mark set by the creator, default 60%) or **practice** (does not block). Questions are drawn at random from the quiz's pool per learner, options shuffled. API done for unit tests. |
| F2.3 | Quiz settings | B | Every setting is the creator's choice: timer, number of attempts, questions per attempt, pass mark, required or practice, open and close dates, one question per screen or all on one page, going back allowed or not, when learners see correct answers (after each question, after submitting, after the quiz closes, never), teacher review on/off, and the anti-cheating layers in section 5. Shown to the learner before starting. API done for timer, attempts, questions, pass mark, review, tracking. |
| F2.12 | Restriction levels | B | The creator starts from a level and can change any setting after (shown as "Custom"). **Practice:** no timer, unlimited attempts, answers shown after each question, no tracking, does not block. **Standard** (default for unit tests): optional timer, 3 attempts, results after submitting, blocks the next unit until passed. **Strict:** timer with auto-submit, 1–2 attempts, one question per screen without going back, tab and paste tracking, copy blocked, teacher review on. **Supervised exam:** Strict plus an access code the teacher gives out in the room, an open window of set times, explain your answer (F2.5) and camera (F2.7) when those exist. |
| F2.4 | Teacher review | B | With review on, the AI's marks are proposals. The teacher sees each answer, the proposed mark and the integrity evidence, and confirms or changes it. The learner gets the result after review. Every change is logged. API done. |
| F2.5 | Explain your answer | Later | After a test, the AI asks two or three follow-up questions about the learner's own answers and reports how consistent answer and explanation are. Evidence for the teacher, never an automatic penalty. |
| F2.6 | Final exam | Later | A quiz placed at the end of the course (F2.2) covering all units, usually at Strict or Supervised level. Passing gives a completion certificate. |
| F2.7 | Camera | Later | Optional per test, off by default. Periodic snapshots (not video) shown to the teacher with the other evidence. The learner is told before starting. |
| F2.8 | Question types | B | Multiple choice (one answer), multiple select, true/false, short answer, written answer. Each question can be tagged with a concept. Multiple choice, multiple select and true/false are marked in code; short and written answers by the AI (a proposal when teacher review is on). |
| F2.9 | Quiz import | B | Import questions into a quiz's pool from a FocusFlow CSV/Excel template, then Moodle **GIFT** and **Aiken** files. The import shows a preview with errors per line before saving. |
| F2.10 | Question editor | B | Add, edit, reorder and delete questions; mark the correct option(s); write an answer guide for written answers. |
| F2.11 | Test screen | B | The learner reads the rules, starts, answers one question per screen or all on one page (creator's choice), sees the timer and submits; then sees the result (or "waiting for review"). |

### M3. Slides as written

The teacher's own slides become the course. No AI is needed; AI only helps
with structure (F3.5) and references (F1.8).

| ID | Feature | Step | Acceptance criteria |
| --- | --- | --- | --- |
| F3.1 | Slide extraction | A | A .pptx is turned into pages, one per slide, without AI: title, bullets with their levels, bold/italic/code styling, pictures (stored in the media bucket), tables and speaker notes. Slides with shape diagrams, SmartArt or charts are flagged. |
| F3.2 | PDF pages | A | A PDF is shown page by page as it looks (rendered in the browser), with the same navigation as slides. |
| F3.3 | Cut into units | A | The creator sees a strip of the file's slides or pages and clicks between two of them to cut. A unit is a range of one material (first and last page) plus any other materials (videos, links, other files). One file can be one unit. Unit titles start from the first slide's title. |
| F3.4 | Suggested cuts from the file | A | Cuts and titles are pre-filled, without AI, from PDF bookmarks, PowerPoint sections and section-header slides when the file has them. Otherwise the file starts as one unit. |
| F3.5 | AI-suggested structure | A | "Suggest units with AI": the AI reads the slides and proposes cuts and titles only (no written text). The creator moves cuts, renames units and accepts. The AI also tags the concepts each slide covers, kept hidden from learners (used by F2.1 and M4 later). Counts against the AI allowance. |
| F3.6 | Page editor | A | The creator sees each extracted page next to the original and can fix its text, hide a page, or mark it "see the original". |
| F3.7 | Slide viewer | A | The learner reads one page at a time inside FocusFlow: "Slide 3 of 5", Previous / Next, progress dots, works on a phone. Next after a unit's last page goes to the unit test, or to the next unit when there is none. The unit counts as finished when the last page is reached and there is no test. |
| F3.8 | Diagram card | A | A flagged slide shows its title and real text plus a card: "This slide has a diagram. See slide 7 in Lecture 3.pptx". For a PDF it opens that page; for a .pptx it downloads the file. Opening .pptx in Microsoft's online viewer is a per-course option, off by default (the file is sent to Microsoft). |
| F3.9 | Slide picture fallback | Later | Slides are also rendered as pictures on the server (LibreOffice), so diagram slides can show the original slide instead of the card. |

### M4. Classes and institutions

| ID | Feature | Step | Acceptance criteria |
| --- | --- | --- | --- |
| F4.1 | Invite link | C | The creator of a private course shares an invite link or code; anyone signed in who opens it joins. The creator can turn the link off and see who joined. |
| F4.2 | Pro plan | Later | Subscription that raises course, material and AI limits. Billing through the payments in section 7. |
| F4.3 | Institution workspace | Later | An institution with lecturers; a lecturer invites a class by roster (student ID, email) and students join the course. |
| F4.4 | Consent and data access | Later | Each student's consent is recorded; the institution admin's approval of data access is recorded. No class results are shown without both. |
| F4.5 | Class results (struggle map) | Later | The lecturer sees a heatmap of students against concepts from test results, and per student their weakest concepts with approved materials to send. Nothing goes to a student without the lecturer's say-so. |

## 5. Anti-cheating layers

1. Different questions per learner (random draw from a pool, shuffled options).
2. Timer and limited attempts; auto-submit at time-out.
3. Browser signals: leaving the tab, leaving fullscreen, pasting; copy-paste blocked in the test. Logged as evidence.
4. Explain your answer (F2.5): the strongest defence against AI-written answers.
5. Teacher review of marks and evidence (F2.4).
6. Camera snapshots (F2.7), later.

## 6. Build order

**Step A: slides as written.** Build mode (F1.10), slide extraction and PDF
pages (F3.1, F3.2), cutting into units with suggested cuts (F3.3–F3.5), page
editor (F3.6), slide viewer and diagram card (F3.7, F3.8).
Done when: a teacher uploads a 40-slide .pptx, gets units suggested from its
sections or by the AI, moves one cut, fixes one page and publishes; a learner
reads the first unit with Next and sees a diagram card that opens the original.
The slide text the learner sees matches the file word for word.

**Step B: quizzes.** One quiz feature (F2.1–F2.3): question types, editor and
import (F2.8–F2.10), AI-created questions from chosen material, restriction
levels (F2.12) and the test screen (F2.11), on the existing engine (F2.4).
Done when: a teacher makes a unit test from 20 imported questions, one written
by hand and five created by the AI from the unit's slides, picks Strict and
shortens the timer (level shows Custom); a learner fails once, passes on the
second attempt, and the next unit opens. A Practice quiz between two units
shows answers after each question and blocks nothing.

**Step C: invite link** (F4.1), so lecturers can pilot with real students.

**Later:** Pro plan, institution workspace, consent, class results, final exam,
explain your answer, camera, slide pictures.

## 7. Business model (pilot; everything subject to validation)

**Free during the pilot and early growth:** joining courses, studying units, unit
tests, the final exam and the **certificate**. Free learning is the point of
difference from Udemy and Coursera; charging starts only once there are enough
users (trigger to be set, e.g. 500 active learners).

| Area | Personal (free) | Cost control |
| --- | --- | --- |
| Learning, progress | Unlimited | No AI involved |
| Slides-as-written courses | Within course limits; extraction and viewing unlimited | No AI unless the creator asks for suggestions |
| Multiple-choice marking | Unlimited | Marked in code |
| Written-answer marking | Unlimited, with a daily attempt limit | Cheapest capable model |
| "Explain your answer" | Final exam only at first | Keeps per-learner AI cost low |
| Certificate | Free | Generated by the app |
| Course creation | **2 courses per user**, each ≤ 15 files / 300 pages, ≤ 3 AI organise runs | Settings, adjustable after the pilot |
| AI allowance | Daily and monthly limit per creator | Admins unlimited |

**Pro (later):** more courses and materials, a larger AI allowance and the AI
features that cost most (AI-written lessons on long materials, more organise
and reference runs). **Institution (later):** priced per lecturer or per
student, includes classes, consent records and class results. Possibly paid
courses (creator sets 500–10,000 RWF, platform keeps ~15%) and paid certificates.

**Payments (later):** accept **cards and mobile money**. Preferred provider:
**Flutterwave** — cards (Visa/Mastercard) and MTN MoMo in RWF through one
checkout; needs a business registered with RDB. pawaPay is the alternative for
MTN + Airtel mobile money. Mobile money has no automatic monthly charges, so use
a top-up wallet (cards can top up the same wallet). Stripe does not support
Rwandan businesses; add it via Stripe Atlas only if international USD card
buyers matter.

**Built now so these stay possible:** a plan field on each user, a course price
field (always 0), course-creation counters and limits, and AI cost logged per
course and per creator.

## 8. AI providers

Behind one internal interface, each task's provider and model is a setting.
Claude reads PDFs and slides, organises, writes questions and marks; Gemini
watches videos and is the backup for every task (a key pool rests keys that hit
limits). Every AI task runs as a background job with retries, a saved status
and a usage log (task, model, tokens, cost, duration, outcome; no learner names).

| Task | AI-written lessons | Slides as written |
| --- | --- | --- |
| Read materials (concepts with sources) | Yes | Only when the creator asks for suggestions (F3.5) |
| Plan units | Yes | Yes, as cuts and titles only |
| Write unit texts | Yes | **Never** |
| Find references | Yes | Yes |
| Suggest questions, mark written answers | Yes | Yes |

## 9. Open questions

- Slide picture fallback (F3.9): add LibreOffice on the VPS now, or wait until real decks show how many slides need it? Current plan: wait.
- Quiz import (F2.9): which format do the pilot lecturers already use? This sets the order of CSV, GIFT and Aiken.

- Microsoft online viewer for .pptx (F3.8): offer it at all, given the file goes to Microsoft?

## 10. Out of scope (for now)

- Video recording of learners, room scans
- Rankings, leaderboards, social features
- Automatic penalties
- The AI rewriting a teacher's slides in "slides as written" courses
