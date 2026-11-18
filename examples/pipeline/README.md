# One-step setup: paperless-gpt and paperless-ai behind paperless-ngx

This folder sets up the whole pipeline described in
[Using it together with paperless-ai](../../README.md#using-it-together-with-paperless-ai) with one
configuration file and one command. It is an add-on for a paperless-ngx that already runs; it does not
install paperless-ngx or a model server.

What the command does:

1. Creates the six pipeline tags in paperless-ngx.
2. Creates the four workflows that pass a document from OCR to classification and clean up afterwards.
3. Writes the first configuration of paperless-ai.
4. Starts paperless-gpt (OCR) and paperless-ai (classification).

Running it again is safe. Tags, workflows and a paperless-ai configuration that already exist are left as
they are.

## What you need

- Docker with the Compose plugin.
- A running paperless-ngx that the containers can reach, and an API token of a user who may change
  documents, tags and workflows. The workflows use the conditions "has all of these tags" and "has none of
  these tags", so paperless-ngx must be recent enough to offer them. Tested with paperless-ngx 3.4.
- A model server: Ollama, or anything with an OpenAI-compatible API. You need one model with vision for OCR
  and one model for classification. They can be the same model.

## Setup

```bash
git clone https://github.com/ivanzud/paperless-gpt.git
cd paperless-gpt/examples/pipeline
cp .env.example .env
# edit .env: PAPERLESS_URL, PAPERLESS_TOKEN, the model server and the two model names
docker compose up -d
```

Check the result of the setup step:

```bash
docker compose logs setup
```

It lists every tag and workflow as `created` or `exists` and ends with `Setup finished`. If something is
wrong with the address or the token, it says so and the two tools are not started.

Then create the login for the paperless-ai web interface once, at <http://localhost:3000/setup>. Until an
account exists, that page is open to anyone who can reach the port, which is why the ports are published on
`127.0.0.1` only by default. Classification already runs without this step.

## The settings in `.env`

| Setting | Meaning |
| --- | --- |
| `PAPERLESS_URL` | Address of paperless-ngx as seen from inside a container |
| `PAPERLESS_TOKEN` | API token used by paperless-gpt and by the setup step |
| `PAPERLESS_AI_TOKEN` | Optional separate token for paperless-ai |
| `PAPERLESS_PUBLIC_URL` | Address of paperless-ngx in the browser, for links |
| `LLM_BACKEND` | `ollama` or `openai` (also for OpenAI-compatible servers) |
| `OLLAMA_URL` | Ollama address, for `LLM_BACKEND=ollama` |
| `OPENAI_BASE_URL`, `OPENAI_API_KEY` | Server and key, for `LLM_BACKEND=openai` |
| `VISION_MODEL` | Model that reads the pages |
| `TEXT_MODEL` | Model that classifies |
| `SCAN_INTERVAL` | How often paperless-ai looks for work, in cron syntax |
| `BIND_ADDRESS` | Where the two web interfaces are published |

`host.docker.internal` in the defaults means "this machine". If paperless-ngx or the model server run in
Docker on the same machine without a published port, put the containers on a shared network instead and
use the service name.

## Trying it out

Upload a document to paperless-ngx. Within a few minutes it gets `paperless-gpt-ocr-auto`, then its text is
replaced by the OCR result, then title, tags, correspondent, document type and date are set, and the
pipeline tags disappear again. A document that ends up with `paperless-gpt-ocr-failed` or
`paperless-ai-failed` needs a look; the logs say why:

```bash
docker compose logs paperless-gpt paperless-ai
```

## Changing things later

- paperless-gpt is configured in `docker-compose.yml`; every setting is described in the
  [main README](../../README.md#environment-variables).
- paperless-ai is configured in its web interface. The setup step writes its configuration only once, so
  changes to the models in `.env` do not reach paperless-ai afterwards.
- Custom fields, restricting the model to existing tags, and prompts are left at simple defaults. Set them in
  the paperless-ai web interface.
- Existing documents are not touched. Add `paperless-gpt-ocr-auto` to a document to send it through the
  pipeline.

## Removing it

```bash
docker compose down        # keeps the settings and the OCR history
docker compose down -v     # removes them as well
```

The tags and workflows stay in paperless-ngx; delete the four workflows named "Pipeline 1" to "Pipeline 4"
and the six tags there if you no longer want them.
