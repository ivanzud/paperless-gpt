#!/usr/bin/env python3
"""One-time setup for the paperless-gpt + paperless-ai pipeline.

Creates the pipeline tags and workflows in paperless-ngx and writes the first
configuration of paperless-ai. Safe to run again: existing tags, workflows
and configuration are left as they are.

Uses the Python standard library only.
"""

import json
import os
import secrets
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

TAGS = {
    "ocr_queue": "paperless-gpt-ocr-auto",
    "ocr_done": "paperless-gpt-ocr-complete",
    "ocr_failed": "paperless-gpt-ocr-failed",
    "ai_queue": "paperless-ai-auto",
    "ai_done": "ai-processed",
    "ai_failed": "paperless-ai-failed",
}

# paperless-ngx workflow constants
TRIGGER_ADDED, TRIGGER_UPDATED = 2, 3
ACTION_ASSIGN, ACTION_REMOVE = 1, 2
ALL_SOURCES = [1, 2, 3, 4]  # consume folder, API upload, mail, web UI

AI_ENV_PATH = os.environ.get("PAPERLESS_AI_ENV_PATH", "/paperless-ai-data/.env")


def fail(message):
    print(f"ERROR: {message}", file=sys.stderr)
    sys.exit(1)


def setting(name, default=None, required=False):
    value = os.environ.get(name, "").strip()
    if not value:
        if required:
            fail(f"{name} is not set. Fill it in in .env.")
        return default
    return value


class Paperless:
    def __init__(self, base_url, token):
        self.base = base_url.rstrip("/")
        self.token = token

    def request(self, method, path, body=None):
        url = path if path.startswith("http") else f"{self.base}{path}"
        data = json.dumps(body).encode() if body is not None else None
        request = urllib.request.Request(url, data=data, method=method)
        request.add_header("Authorization", f"Token {self.token}")
        request.add_header("Accept", "application/json; version=9")
        if data is not None:
            request.add_header("Content-Type", "application/json")
        with urllib.request.urlopen(request, timeout=30) as response:
            text = response.read().decode()
        return json.loads(text) if text else None

    def everything(self, path):
        items, url = [], path
        while url:
            page = self.request("GET", url)
            items.extend(page["results"])
            url = page.get("next")
            if url:
                # "next" carries the address paperless-ngx believes it has,
                # which is not always reachable from here.
                parts = urllib.parse.urlsplit(url)
                url = f"{parts.path}?{parts.query}"
        return items

    def wait_until_ready(self, seconds=180):
        deadline = time.monotonic() + seconds
        while True:
            try:
                self.request("GET", "/api/tags/?page_size=1")
                return
            except urllib.error.HTTPError as error:
                if error.code in (401, 403):
                    fail(
                        "paperless-ngx rejected PAPERLESS_TOKEN "
                        f"(HTTP {error.code}). Check the token and that its "
                        "user may view and change tags."
                    )
                problem = f"HTTP {error.code}"
            except (urllib.error.URLError, OSError, ValueError) as error:
                problem = str(getattr(error, "reason", error))
            if time.monotonic() > deadline:
                fail(
                    f"paperless-ngx did not answer at {self.base} ({problem}). "
                    "Check PAPERLESS_URL; it must be reachable from inside a "
                    "container."
                )
            print(f"Waiting for paperless-ngx at {self.base} ({problem}) ...")
            time.sleep(5)


def describe(error):
    try:
        return error.read().decode()[:400]
    except Exception:  # noqa: BLE001 - best effort for an error message
        return ""


def ensure_tags(paperless):
    existing = {tag["name"].lower(): tag["id"] for tag in paperless.everything("/api/tags/?page_size=100")}
    ids = {}
    for key, name in TAGS.items():
        if name.lower() in existing:
            ids[key] = existing[name.lower()]
            print(f"tag       exists   {name}")
            continue
        try:
            created = paperless.request(
                "POST", "/api/tags/", {"name": name, "matching_algorithm": 0, "is_inbox_tag": False}
            )
        except urllib.error.HTTPError as error:
            fail(f"Could not create the tag {name}: HTTP {error.code} {describe(error)}")
        ids[key] = created["id"]
        print(f"tag       created  {name}")
    return ids


def workflows(tag):
    def trigger(kind, **filters):
        return {"type": kind, "sources": ALL_SOURCES, "matching_algorithm": 0, **filters}

    return [
        {
            "name": "Pipeline 1: queue new documents for OCR",
            "triggers": [
                trigger(TRIGGER_ADDED, filter_has_not_tags=[tag["ocr_queue"], tag["ocr_done"], tag["ocr_failed"]])
            ],
            "actions": [{"type": ACTION_ASSIGN, "assign_tags": [tag["ocr_queue"]]}],
        },
        {
            "name": "Pipeline 2: hand over from OCR to classification",
            "triggers": [
                trigger(
                    TRIGGER_UPDATED,
                    filter_has_all_tags=[tag["ocr_done"]],
                    filter_has_not_tags=[tag["ai_done"], tag["ai_queue"]],
                )
            ],
            "actions": [{"type": ACTION_ASSIGN, "assign_tags": [tag["ai_queue"]]}],
        },
        {
            "name": "Pipeline 3: clean up after classification",
            "triggers": [trigger(TRIGGER_UPDATED, filter_has_all_tags=[tag["ai_done"], tag["ocr_done"]])],
            "actions": [
                {"type": ACTION_REMOVE, "remove_tags": [tag["ai_done"], tag["ai_queue"], tag["ocr_done"]]}
            ],
        },
        {
            "name": "Pipeline 4: clean up after classification without OCR",
            "triggers": [
                trigger(
                    TRIGGER_UPDATED,
                    filter_has_all_tags=[tag["ai_done"], tag["ai_queue"]],
                    filter_has_not_tags=[tag["ocr_done"]],
                )
            ],
            "actions": [{"type": ACTION_REMOVE, "remove_tags": [tag["ai_done"], tag["ai_queue"]]}],
        },
    ]


def ensure_workflows(paperless, tag_ids):
    try:
        existing = {item["name"] for item in paperless.everything("/api/workflows/?page_size=100")}
    except urllib.error.HTTPError as error:
        fail(
            f"Could not read the workflows: HTTP {error.code}. The token's user "
            "needs permission to view and add workflows."
        )
    order = len(existing)
    for workflow in workflows(tag_ids):
        if workflow["name"] in existing:
            print(f"workflow  exists   {workflow['name']}")
            continue
        order += 1
        try:
            created = paperless.request("POST", "/api/workflows/", {**workflow, "order": order, "enabled": True})
        except urllib.error.HTTPError as error:
            fail(
                f"Could not create the workflow \"{workflow['name']}\": HTTP {error.code} "
                f"{describe(error)}\nThe pipeline needs a paperless-ngx version whose workflows "
                "can filter on \"has all of these tags\" and \"has none of these tags\"."
            )
        check_workflow(workflow, created)
        print(f"workflow  created  {workflow['name']}")


def check_workflow(wanted, created):
    """A server that ignores a filter it does not know would create a
    workflow that fires on every document, so compare what was stored."""
    for wanted_trigger, stored in zip(wanted["triggers"], created.get("triggers", [])):
        for key, value in wanted_trigger.items():
            if key.startswith("filter_") and sorted(stored.get(key) or []) != sorted(value):
                fail(
                    f"paperless-ngx did not store the condition {key} of the workflow "
                    f"\"{wanted['name']}\". Delete that workflow and update paperless-ngx."
                )


def env_line(key, value):
    return f"{key}={value}"


def write_ai_config(paperless_url, token):
    if os.path.exists(AI_ENV_PATH):
        print("config    exists   paperless-ai (left unchanged)")
        return

    backend = setting("LLM_BACKEND", "ollama").lower()
    text_model = setting("TEXT_MODEL", required=True)
    values = {
        "PAPERLESS_API_URL": f"{paperless_url.rstrip('/')}/api",
        "PAPERLESS_API_TOKEN": token,
        "PAPERLESS_EXTERNAL_URL": setting("PAPERLESS_PUBLIC_URL", ""),
        "SCAN_INTERVAL": setting("SCAN_INTERVAL", "*/5 * * * *"),
        # Only documents that passed OCR are classified.
        "PROCESS_PREDEFINED_DOCUMENTS": "yes",
        "TAGS": TAGS["ai_queue"],
        "ADD_AI_PROCESSED_TAG": "yes",
        "AI_PROCESSED_TAG_NAME": TAGS["ai_done"],
        "BLOCK_SCAN_WHEN_TAGS_PRESENT": TAGS["ocr_queue"],
        "AI_FAILED_TAG_NAME": TAGS["ai_failed"],
        "MAX_PROCESSING_ATTEMPTS": "3",
        "DISABLE_AUTOMATIC_PROCESSING": "no",
        # What the model may set.
        "ACTIVATE_TAGGING": "yes",
        "ACTIVATE_CORRESPONDENTS": "yes",
        "ACTIVATE_DOCUMENT_TYPE": "yes",
        "ACTIVATE_TITLE": "yes",
        "ACTIVATE_CUSTOM_FIELDS": "no",
        "CUSTOM_FIELDS": '{"custom_fields":[]}',
        "USE_EXISTING_DATA": "yes",
        "USE_PROMPT_TAGS": "no",
        "RESTRICT_TO_EXISTING_TAGS": "no",
        "RESTRICT_TO_EXISTING_CORRESPONDENTS": "no",
        "RESTRICT_TO_EXISTING_DOCUMENT_TYPES": "no",
        "JWT_SECRET": secrets.token_hex(32),
        "API_KEY": secrets.token_hex(32),
    }
    if backend == "ollama":
        values.update(
            {
                "AI_PROVIDER": "ollama",
                "OLLAMA_API_URL": setting("OLLAMA_URL", required=True),
                "OLLAMA_MODEL": text_model,
            }
        )
    elif backend == "openai":
        values.update(
            {
                "AI_PROVIDER": "custom",
                "CUSTOM_BASE_URL": setting("OPENAI_BASE_URL", required=True),
                "CUSTOM_API_KEY": setting("OPENAI_API_KEY", "none"),
                "CUSTOM_MODEL": text_model,
            }
        )
    else:
        fail(f"LLM_BACKEND must be \"ollama\" or \"openai\", not \"{backend}\".")

    os.makedirs(os.path.dirname(AI_ENV_PATH), exist_ok=True)
    temporary = f"{AI_ENV_PATH}.tmp"
    with open(temporary, "w", encoding="utf-8") as handle:
        handle.write("\n".join(env_line(key, value) for key, value in values.items()) + "\n")
    os.chmod(temporary, 0o600)
    os.chown(temporary, 1000, 1000)
    os.replace(temporary, AI_ENV_PATH)
    print("config    created  paperless-ai")


def main():
    url = setting("PAPERLESS_URL", required=True)
    token = setting("PAPERLESS_TOKEN", required=True)
    setting("VISION_MODEL", required=True)
    setting("TEXT_MODEL", required=True)

    paperless = Paperless(url, token)
    paperless.wait_until_ready()
    tag_ids = ensure_tags(paperless)
    ensure_workflows(paperless, tag_ids)
    write_ai_config(url, setting("PAPERLESS_AI_TOKEN", token))
    print("Setup finished. New documents in paperless-ngx are now read and classified automatically.")


if __name__ == "__main__":
    main()
