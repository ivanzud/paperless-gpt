# AGENTS.md

paperless-gpt is a Go backend with a React/TypeScript frontend (in `web-app/`) that provides AI-powered document processing for paperless-ngx: OCR enhancement, title/tag generation, and metadata extraction via LLMs.

## Project structure

```
paperless-gpt/
├── main.go                    # Go backend entry point (web server on port 8080)
├── ocr/                       # OCR provider implementations
├── default_prompts/           # Default AI prompt templates
├── web-app/                   # React/TypeScript frontend
│   ├── src/                   # React source code
│   ├── dist/                  # Built frontend (created by npm run build)
│   └── e2e/                   # Playwright E2E tests
├── docs/                      # Documentation
├── .github/workflows/         # CI/CD pipelines
└── Dockerfile                 # Multi-stage Docker build
```

## Setup and build

No system PDF library is needed: PDF pages are rendered with PDFium through go-pdfium's WebAssembly runtime (`internal/pdfrender`). A C compiler is still required for CGO (go-sqlite3).

Build the frontend first — the Go binary embeds it from `dist/` at the repo root:

```bash
cd web-app && npm install && npm run build && cp -r dist ../ && cd ..
go mod download
go build -o paperless-gpt
```

Full pipeline takes ~2 minutes. Builds and tests are slow but reliable — never cancel a long-running build; use generous timeouts (npm: 300s+, go build/test: 600s+, docker build: 1800s+).

## Testing

```bash
go test ./...                  # Backend tests, ~1 min
cd web-app && npm run lint     # Frontend linting (ESLint)
gofmt -l .                     # Go formatting check (must print nothing)
```

- `npm test` in `web-app/` is currently a placeholder (`echo "TODO"`) — frontend unit tests don't exist yet.
- Some Go tests need network access (tiktoken encoding downloads); failures from that are expected in restricted environments and are not code issues.

### E2E tests (Playwright + TestContainers, requires Docker)

```bash
cd web-app
npm run test:e2e:mock          # Secret-free mock-LLM run (no API keys needed) — use this by default
npm run test:e2e               # Full suite; real-LLM specs need API keys
```

## Running the application

Minimal configuration:

```bash
export PAPERLESS_BASE_URL="http://localhost:8000"
export PAPERLESS_API_TOKEN="your_token_here"
export LLM_PROVIDER="ollama"
export LLM_MODEL="test_model"
export OLLAMA_HOST="http://localhost:11434"
./paperless-gpt                # Web UI at http://localhost:8080
```

The app starts gracefully without a reachable paperless-ngx instance. On first run it creates `prompts/` from `default_prompts/`. Any OpenAI-compatible endpoint works via `OPENAI_BASE_URL`.

To validate a change end-to-end: start the app, verify the web server comes up on port 8080, check the API routes in the startup logs, and confirm the frontend is served from `dist/`.

## Common pitfalls

- **Frontend changes not visible**: re-run `npm run build` and `cp -r dist ../` — the Go binary serves the copied `dist/`, not `web-app/dist/`.
- **Docker build fails**: needs external network access (Alpine repositories); `docker build -t paperless-gpt .` takes 5+ minutes.

## CI and PR guidelines

- `.github/workflows/docker-build-and-push.yml` — secret-free PR pipeline: Go/frontend tests, multi-arch Docker builds (AMD64 + ARM64), mock-LLM E2E. Registry pushes and secrets only run outside `pull_request`.
- `.github/workflows/e2e-real-llm.yml` — real-LLM E2E behind an environment-approval gate (maintainer-triggered).
- Before committing: run `cd web-app && npm run lint` and check `gofmt -l .` reports nothing.
- Commit messages follow Conventional Commits (`feat:`, `fix:`, `ci:`, `docs:` — see `git log`).

## Release communication

Release notes are written by hand. For significant releases (not every patch release), end the GitHub release notes with this footer, after the changelog and credits. Don't edit past releases.

```md
---

☁️ **Want paperless-gpt without hosting it yourself?**

paperless-gpt remains free and fully self-hostable. For users in Germany, Austria and Switzerland, our Fair Hosting Partner [server.camp](https://server.camp/product/paperless-ngx) offers a managed paperless-ngx + paperless-gpt setup and shares part of the revenue with the open source project.
```

paperless-gpt's current Fair Hosting Partner for Germany, Austria and Switzerland is server.camp. When writing about it anywhere:

- paperless-gpt stays fully self-hostable and MIT licensed; never present self-hosting as the worse option.
- server.camp is a managed hosting option for the DACH region, not the official, exclusive or global host.
- Describe the relationship as Fair Hosting / open source revenue sharing. Never use affiliate or referral wording (affiliate, referral, commission, "use our link").
- Don't publish revenue-share percentages, and don't claim SLAs, certifications, backup schedules or security guarantees on server.camp's behalf, without maintainer approval.

This project uses **bd** (beads) for issue tracking. Run `bd onboard` to get started.

## Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work atomically
bd close <id>         # Complete work
bd sync               # Sync with git
```

## Non-Interactive Shell Commands

**ALWAYS use non-interactive flags** with file operations to avoid hanging on confirmation prompts.

Shell commands like `cp`, `mv`, and `rm` may be aliased to include `-i` (interactive) mode on some systems, causing the agent to hang indefinitely waiting for y/n input.

**Use these forms instead:**
```bash
# Force overwrite without prompting
cp -f source dest           # NOT: cp source dest
mv -f source dest           # NOT: mv source dest
rm -f file                  # NOT: rm file

# For recursive operations
rm -rf directory            # NOT: rm -r directory
cp -rf source dest          # NOT: cp -r source dest
```

**Other commands that may prompt:**
- `scp` - use `-o BatchMode=yes` for non-interactive
- `ssh` - use `-o BatchMode=yes` to fail instead of prompting
- `apt-get` - use `-y` flag
- `brew` - use `HOMEBREW_NO_AUTO_UPDATE=1` env var

<!-- BEGIN BEADS INTEGRATION -->
## Issue Tracking with bd (beads)

**IMPORTANT**: This project uses **bd (beads)** for ALL issue tracking. Do NOT use markdown TODOs, task lists, or other tracking methods.

### Why bd?

- Dependency-aware: Track blockers and relationships between issues
- Git-friendly: Auto-syncs to JSONL for version control
- Agent-optimized: JSON output, ready work detection, discovered-from links
- Prevents duplicate tracking systems and confusion

### Quick Start

**Check for ready work:**

```bash
bd ready --json
```

**Create new issues:**

```bash
bd create "Issue title" --description="Detailed context" -t bug|feature|task -p 0-4 --json
bd create "Issue title" --description="What this issue is about" -p 1 --deps discovered-from:bd-123 --json
```

**Claim and update:**

```bash
bd update <id> --claim --json
bd update bd-42 --priority 1 --json
```

**Complete work:**

```bash
bd close bd-42 --reason "Completed" --json
```

### Issue Types

- `bug` - Something broken
- `feature` - New functionality
- `task` - Work item (tests, docs, refactoring)
- `epic` - Large feature with subtasks
- `chore` - Maintenance (dependencies, tooling)

### Priorities

- `0` - Critical (security, data loss, broken builds)
- `1` - High (major features, important bugs)
- `2` - Medium (default, nice-to-have)
- `3` - Low (polish, optimization)
- `4` - Backlog (future ideas)

### Workflow for AI Agents

1. **Check ready work**: `bd ready` shows unblocked issues
2. **Claim your task atomically**: `bd update <id> --claim`
3. **Work on it**: Implement, test, document
4. **Discover new work?** Create linked issue:
   - `bd create "Found bug" --description="Details about what was found" -p 1 --deps discovered-from:<parent-id>`
5. **Complete**: `bd close <id> --reason "Done"`

### Auto-Sync

bd automatically syncs with git:

- Exports to `.beads/issues.jsonl` after changes (5s debounce)
- Imports from JSONL when newer (e.g., after `git pull`)
- No manual export/import needed!

### Important Rules

- ✅ Use bd for ALL task tracking
- ✅ Always use `--json` flag for programmatic use
- ✅ Link discovered work with `discovered-from` dependencies
- ✅ Check `bd ready` before asking "what should I work on?"
- ❌ Do NOT create markdown TODO lists
- ❌ Do NOT use external issue trackers
- ❌ Do NOT duplicate tracking systems

For more details, see README.md and docs/QUICKSTART.md.

<!-- END BEADS INTEGRATION -->

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd sync
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds
