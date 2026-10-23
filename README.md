# FocusFlow API

One Go service for FocusFlow: accounts, courses and course materials. It
replaces the Laravel auth service and the two Go microservices (course,
course-material). The Nuxt frontend stays on Vercel and talks to this API
through its `server/api/*` proxy routes.

Production: `https://api.focusflow.sswoco.org` on the shared VPS (Caddy → `127.0.0.1:8091`).

## Layout

```
cmd/api                 main: config, DB, migrations, routes, server
internal/auth           users, sessions, login/register, Google, password reset,
                        email verification, profile/avatar, admin users + analytics
internal/course         courses, categories, enrollments, invitations, course analytics
internal/material       lessons, YouTube search, transcripts, quizzes (Gemini),
                        reflections, takeaways, dashboard, material analytics
internal/platform       config, db, logger, tracing, metrics, mailer, storage
deploy                  docker-compose for the VPS
scripts/smoke.sh        end-to-end test against a running API
```

## Auth

- `POST /login` and `POST /register` set an httpOnly `ff_session` cookie
  (random token; only its SHA-256 is stored in `sessions`, 30-day sliding expiry).
  `Authorization: Bearer <token>` works too.
- Every user-scoped route takes the user from the session. `user_id` in a
  query or body is ignored.
- Laravel bcrypt hashes (`$2y$`) verify as they are.
- Roles: `student` (default), `teacher`, `admin`. Admin routes live under `/api/admin`.
- Validation errors look like Laravel's: `422 {"message": "...", "errors": {"field": ["..."]}}`.

## Run locally

```bash
docker run -d --name ff-db -e POSTGRES_PASSWORD=dev -e POSTGRES_DB=focusflow -p 5432:5432 postgres:16-alpine
cp .env.example .env    # set DATABASE_URL and APP_KEY (openssl rand -hex 32)
go run ./cmd/api        # tables are created on start
BASE=http://localhost:8091 QUIZ_STATUS=503 ./scripts/smoke.sh
```

Without `SMTP_HOST`, emails are written to the log instead of sent. Without
`GEMINI_API_KEY`, quiz and reflection endpoints return 503.

## Deploy

Push to `main`: GitHub Actions runs vet and tests, builds the image to
`ghcr.io/foucflow/focusflow-api`, then on the VPS copies `deploy/docker-compose.yml`
to `/opt/focusflow`, pulls and restarts, and checks `/health`.
Secrets needed in the repo: `VPS_HOST`, `VPS_SSH_KEY`. The app's `.env` lives only in
`/opt/focusflow/.env` on the VPS.

## Route changes from the old services

- `GET /api/admin/analytics/overview` is the user overview (from Laravel); the course
  overview moved to `GET /api/admin/analytics/courses/overview`.
- `/api/user/{id}` (GET/PUT), `/api/user/{id}/avatar` and every course-material,
  quiz, reflection, takeaway and dashboard route now require login.
- Lessons can only be created, changed or deleted by the course owner or an admin.
- `/sanctum/csrf-cookie` is gone: the browser never calls the API directly, so the
  proxy needs no CSRF handshake.
