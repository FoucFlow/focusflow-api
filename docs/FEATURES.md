# FocusFlow: Feature Specification

Version 0.2 · 24 October 2026 · replaces the lecturer-only draft v0.1

Feature IDs are stable: use them in commits, issues and pull requests.

## 1. What FocusFlow is

Anyone can **create a course** from their own materials or **take a public course**
someone else made. Self-taught learners and teachers use the same app.

1. The creator drops materials onto a course canvas: PDF notes, PowerPoint
   slides, video links and web links.
2. The AI reads them and proposes a course divided into **units**. Each unit has
   a unit text, its videos, and links to the matching notes and slides. The
   creator edits, accepts and publishes.
3. The AI can suggest extra materials per unit; the creator approves each one.
4. Each unit ends with a **unit test**; the course ends with a **final exam**.
   Learners must finish a unit before the next one opens.
5. Tests are protected against cheating in layers (section 5). When the creator
   turns on **teacher review**, the AI's marks are proposals the teacher confirms
   or rejects.

## 2. Roles

| Role | Who | Can do |
| --- | --- | --- |
| Creator | Any signed-in user, for courses they made | Build, organise and publish the course; set test rules; review marks |
| Learner | Any signed-in user who joined a course | Study units in order, take tests, see their own progress |
| Admin | Platform staff | Manage users, handle reported courses, see aggregate stats |

A user can be a creator of some courses and a learner in others.

## 3. Principles

- The creator is in control: AI drafts, the creator decides. Nothing AI-made reaches learners until approved.
- AI gives explanations and hints, not test answers.
- The AI never decides that someone cheated. It flags evidence; a person decides.
- Learners see a test's rules (timer, tracking, camera) before they start.
- Send the minimum to the AI model; never send a learner's name.
- Everything is exportable in open formats.

## 4. Features

Priorities: **S1** = step 1 (now), **S2–S5** = later steps, in order.

### M1. Course section (step 1)

| ID | Feature | Step | Acceptance criteria |
| --- | --- | --- | --- |
| F1.1 | Create course | S1 | Title, description, category, language, visibility (private by default). The creator owns it. |
| F1.2 | Course canvas: materials | S1 | Upload PDF (≤ 25 MB) and PowerPoint .pptx (≤ 25 MB); add video links (YouTube) and web links. Types and sizes are validated; a failed upload says why. Each material has a title and type. |
| F1.3 | Material digest | S1 | After upload, each material is read in the background (PDF by the AI directly, .pptx slide text extracted first, video by its title and description) and a short digest is stored: topics and key points. Status: processing, ready or failed (with reason). |
| F1.4 | AI organise into units | S1 | One click proposes units from the ready materials: for each unit a title, a unit text (explanation written from the materials), and which materials and videos belong to it, in order. The proposal is a draft; the creator can rename, reorder, edit text, move materials, add or delete units, or discard and run again. |
| F1.5 | Units | S1 | Units have an order, title, text (Markdown) and linked materials. Learners only see published courses. |
| F1.6 | Publish and browse | S1 | The creator publishes; public courses appear in Browse. Before going public the creator confirms they may share the uploaded files. |
| F1.7 | Learning flow | S1 | A learner joins a course and opens units in order. Unit 1 is open; each next unit opens when the previous one is finished. Until tests exist (S3), "finished" = the learner marks the unit done. Progress is visible to the learner. |
| F1.8 | AI-suggested materials | S2 | Per unit, the AI suggests videos and links with source, a short summary and why. The creator approves or rejects each; approver and time are recorded. |
| F1.9 | Report a course | S2 | Any user can report a public course; admins review. |

### M2. Tests and exams

| ID | Feature | Step | Acceptance criteria |
| --- | --- | --- | --- |
| F2.1 | Question pool per unit | S3 | AI generates a pool from the unit's materials (multiple choice, short answer, explain). The creator reviews and can edit or remove questions. |
| F2.2 | Unit test | S3 | Drawn at random from the pool per learner, options shuffled. Passing (pass mark set by the creator, default 60%) finishes the unit and opens the next. |
| F2.3 | Test settings | S3 | Timer, number of attempts, pass mark, teacher review on/off, tab-switch and paste tracking on/off. Shown to the learner before starting. |
| F2.4 | Teacher review | S3 | With review on, the AI's marks are proposals. The teacher sees each answer, the proposed mark and the integrity evidence, and confirms or changes it. The learner gets the result after review. Every change is logged. |
| F2.5 | Explain your answer | S4 | After a test, the AI asks two or three follow-up questions about the learner's own answers and reports how consistent answer and explanation are. Evidence for the teacher, never an automatic penalty. |
| F2.6 | Final exam | S4 | Covers all units, same engine and settings as unit tests. Passing gives a completion certificate. |
| F2.7 | Camera | S5 | Optional per test, off by default. Periodic snapshots (not video) shown to the teacher with the other evidence. The learner is told before starting. |

## 5. Anti-cheating layers

1. Different questions per learner (random draw from a pool, shuffled options).
2. Timer and limited attempts; auto-submit at time-out.
3. Browser signals: leaving the tab, leaving fullscreen, pasting; copy-paste blocked in the test. Logged as evidence.
4. Explain your answer (F2.5): the strongest defence against AI-written answers.
5. Teacher review of marks and evidence (F2.4).
6. Camera snapshots (F2.7), later.

No layer applies a penalty on its own. The teacher decides.

## 6. Step 1 build plan

Backend (`internal/learn`): tables `courses` (new fields), `course_resources`,
`units`, `unit_resources`, `unit_progress`; endpoints for materials, digests,
organise, units, publish, join and progress. Files go to the media bucket;
AI calls use Gemini with structured JSON output. Frontend: course canvas,
unit editor, course page with locked/unlocked units.

Done when: a user creates a course, uploads a PDF, a .pptx and a video link,
clicks Organise, edits the draft, publishes; a second user joins, finishes
unit 1, and unit 2 opens.

## 7. Business model (pilot; everything subject to validation)

**Free during the pilot and early growth:** joining courses, studying units, unit
tests, the final exam and the **certificate**. Free learning is the point of
difference from Udemy and Coursera; charging starts only once there are enough
users (trigger to be set, e.g. 500 active learners).

| Area | Free tier | Cost control |
| --- | --- | --- |
| Learning, progress | Unlimited | No AI involved |
| Multiple-choice marking | Unlimited | Marked in code |
| Written-answer marking | Unlimited, with a daily attempt limit | Cheapest capable model (Claude Haiku 5.5) |
| "Explain your answer" | Final exam only at first | Keeps per-learner AI cost low |
| Certificate | Free | Generated by the app |
| Course creation | **2 courses per user**, each ≤ 15 files / 300 pages, ≤ 3 AI organise runs | Settings, adjustable after the pilot |

**Later (not built now):** creator plan for more courses, paid courses (creator
sets 500–10,000 RWF, platform keeps ~15%), possibly paid certificates.

**Payments (later):** accept **cards and mobile money**. Preferred provider:
**Flutterwave** — cards (Visa/Mastercard) and MTN MoMo in RWF through one
checkout; needs a business registered with RDB. pawaPay is the alternative for
MTN + Airtel mobile money. Mobile money has no automatic monthly charges, so use
a top-up wallet (cards can top up the same wallet). Stripe does not support
Rwandan businesses; add it via Stripe Atlas only if international USD card
buyers matter.

**Built now so these stay possible:** a plan field on each user, a course price
field (always 0), course-creation counters and limits, and AI cost logged per
course.

## 8. AI providers

Behind one internal interface, each task's provider and model is a setting:
Claude for reading PDFs/slides, organising units, questions and marking
(Opus 5.5 for organising, Haiku 5.5 for digests and marking); Gemini only for
videos without captions; captions are read directly when available. Every AI
task runs as a background job with retries, a saved status and a usage log
(task, model, tokens, cost, duration, outcome; no learner names).

## 9. Out of scope (for now)

- Institution workspaces, the lecturer struggle map and department reports (from v0.1; may return as a teacher view on test results)
- Video recording of learners, room scans
- Rankings, leaderboards, social features
- Automatic penalties
