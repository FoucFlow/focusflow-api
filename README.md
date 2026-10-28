# FocusFlow API

The backend for [FocusFlow](https://www.focus-flow.studio), a learning and assessment platform. Anyone can turn their own material (PDF, PowerPoint, YouTube videos, links) into a course of short units that learners finish one at a time. Lecturers and schools can add unit quizzes and final exams, invite a class, and see how each student is doing.

This repository is one Go service: accounts, courses, the course builder, quizzes and exams, institutions and the admin tools. The web app lives in [Focusflow-frontend](https://github.com/Mubaraklouis/Focusflow-frontend).

## What it does

**Courses, two ways to build them**

| Build mode | For | What learners read | What the AI does |
| --- | --- | --- | --- |
| Slides as written (default) | Teachers with an existing PowerPoint or PDF course | The teacher's slides, one page at a time, as written | Suggests where units start and end, and finds outside references. It never changes the teaching text. |
| AI-written lessons | Self-learners turning videos and notes into a course | Unit texts the AI writes from the materials | Reads the materials, organises them into units and drafts the lessons. The creator edits and approves. |

- Upload PDFs and `.pptx` files (straight from the browser to storage with a signed token), add YouTube videos and links.
- `.pptx` slides are parsed into pages (title, bullets, images, tables); PDFs are shown as rendered pages. A page editor lets the creator fix what the parser got wrong.
- Long PDFs and videos are read in parts, so a course can be published early and grows as the rest is read.
- Units unlock one at a time. Learners join for free with a link or an invite code.

**Quizzes, tests and exams**

- A quiz under each unit and a final exam, always last.
- Multiple choice and true/false questions, written in the app or imported from CSV, GIFT or Aiken files.
- Test rules the learner sees before starting: time window, timer, access code, when answers are shown.
- "Explain your answer" and optional camera snapshots. The AI can suggest marks, but a person confirms or rejects them, and the AI never decides that someone cheated.
- Certificates with a public verification page.

**Institutions**

- An institution has owners, lecturers and students. Lecturers invite a class, students consent to sharing results, and lecturers see results per student.
- Plans (free, Pro, Institution) are set by an admin. There are no payments yet.

**AI, with limits**

- Claude reads documents and organises courses; Gemini watches YouTube videos and returns concepts with timestamps. Either one can stand in for the other.
- Every AI call is logged with its tokens and estimated cost. A daily and monthly allowance per creator and a monthly budget for the whole platform stop runaway spending.
- `AI_PROVIDER=fake` runs everything without calling any model, which is what the tests use.

## Stack

- Go 1.25, [Gin](https://github.com/gin-gonic/gin), GORM and pgx on PostgreSQL 16
- S3-compatible object storage for uploads (RustFS in production, any S3 API works)
- Anthropic and Google GenAI SDKs; YouTube Data API
- OpenTelemetry traces, Prometheus metrics at `/metrics`, structured JSON logs
- Docker (static binary in a distroless image), GitHub Actions, Docker Compose on a VPS behind Caddy

## Layout

```
cmd/api              entry point: config, database, migrations, routes, server
internal/auth        users, sessions, login and register, Google sign-in,
                     password reset, email verification, profile, admin users
internal/learn       course builder: materials, PDF and slide reading, AI
                     digests, organising into units, references, quizzes and
                     exams, quiz import, certificates, institutions, invites,
                     AI allowance and call log
internal/course      the older course, category and enrollment routes
internal/material    the older lesson, YouTube and quiz routes
internal/platform    config, database, logger, tracing, metrics, mailer, storage
deploy               docker-compose for production
docs/FEATURES.md     the feature specification
scripts              end-to-end smoke tests against a running API
```

## Run it locally

You need Go 1.25 and Docker.

```bash
# 1. Postgres
docker run -d --name ff-db -e POSTGRES_PASSWORD=dev -e POSTGRES_DB=focusflow \
  -p 5432:5432 postgres:16-alpine

# 2. Settings
cp .env.example .env
#   DATABASE_URL=postgres://postgres:dev@localhost:5432/focusflow?sslmode=disable
#   APP_KEY=$(openssl rand -hex 32)
#   AI_PROVIDER=fake          # no AI keys needed
#   ADMIN_EMAIL / ADMIN_PASSWORD to seed an admin account

# 3. Start (tables are created on start)
go run ./cmd/api
curl http://localhost:8091/health
```

Optional pieces:

- **Uploads:** point `S3_ENDPOINT` and the `AWS_*` values at any S3-compatible store (MinIO, RustFS or AWS S3).
- **Email:** without `SMTP_HOST`, emails are written to the log instead of sent.
- **Real AI:** set `ANTHROPIC_API_KEY` and/or `GEMINI_API_KEY` and leave `AI_PROVIDER` empty. Real calls cost money; the per-user allowance (`AI_USER_DAILY_USD`, `AI_USER_MONTHLY_USD`) and `AI_MONTHLY_BUDGET_USD` cap it.

## Tests

```bash
go vet ./...
go test ./...
```

The unit tests cover slide and PDF parsing, quiz rules and import, the course service and the Gemini key pool. The `scripts/` folder holds end-to-end smoke tests that drive a running API through its HTTP routes (courses, building, slides, quizzes, institutions, plans), for example:

```bash
BASE=http://localhost:8091 ./scripts/smoke-learn.sh
```

## Security notes

- Login sets an httpOnly `ff_session` cookie. It holds a random token, and only its SHA-256 hash is stored. `Authorization: Bearer <token>` works too.
- Every user-scoped route takes the user from the session. A `user_id` sent in a query or body is ignored.
- Roles are checked on the server for every course, unit, quiz and institution route.
- The image contains no secrets. Settings come from environment variables at runtime.

## Deployment

A push to `main` runs `go vet` and the tests, builds the image and pushes it to a private GitHub Container Registry package, then deploys over SSH with Docker Compose and checks `/health` before it finishes. See `.github/workflows/deploy.yml` and `deploy/docker-compose.yml`.

## Status

FocusFlow is in early use. The course builder, quizzes, slides, institutions and plans work in production. Not built yet: reporting a course, rendering diagrams and charts from slides, and payments. The plan is in [docs/FEATURES.md](docs/FEATURES.md).

## License

[GNU Affero General Public License v3.0](LICENSE). You can use, study, change and share this code. If you run a changed version for other people over a network, you must publish your changes under the same license.

## Author

Built by [Mobarak Kuol Louis](https://www.linkedin.com/in/mobarak-kuol-louis), Software Engineer, Kigali.
