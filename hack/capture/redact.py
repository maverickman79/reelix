#!/usr/bin/env python3
"""Turn a raw mitmproxy HAR capture into redacted, committable fixtures.

The raw capture contains live access tokens, device identifiers, and the
administrator password in cleartext on the authenticate call. Nothing from
captures/ is ever committed. This script is the only path from capture to
testdata/.

Usage:
    ./redact.py captures/session.har internal/compat/jellyfin/testdata

Standard library only, by design.
"""

from __future__ import annotations

import json
import pathlib
import re
import sys
from urllib.parse import urlsplit, parse_qsl

PLACEHOLDER = "REDACTED"

# Headers dropped entirely, matched case-insensitively.
SENSITIVE_HEADERS = {
    "authorization",
    "x-emby-authorization",
    "x-mediabrowser-token",
    "x-emby-token",
    "cookie",
    "set-cookie",
}

# Query parameters replaced with the placeholder.
SENSITIVE_QUERY = {"api_key", "apikey", "x-emby-token", "password", "pw"}

# JSON body keys replaced with the placeholder, matched case-insensitively at
# any depth.
SENSITIVE_KEYS = {
    "pw",
    "password",
    "sha1",
    "accesstoken",
    "token",
    "apikey",
    "currentpw",
    "newpw",
}

# Values pseudonymised rather than removed, so that cross-request consistency
# is preserved in fixtures (the same device id maps to the same fake id).
PSEUDONYM_KEYS = {"deviceid", "device_id"}

_pseudonyms: dict[str, str] = {}


def pseudonym(value: str) -> str:
    if value not in _pseudonyms:
        _pseudonyms[value] = f"device-{len(_pseudonyms) + 1:03d}"
    return _pseudonyms[value]


def scrub_json(node):
    """Recursively redact sensitive keys in a decoded JSON structure."""
    if isinstance(node, dict):
        out = {}
        for key, value in node.items():
            low = key.lower()
            if low in SENSITIVE_KEYS:
                out[key] = PLACEHOLDER
            elif low in PSEUDONYM_KEYS and isinstance(value, str):
                out[key] = pseudonym(value)
            else:
                out[key] = scrub_json(value)
        return out
    if isinstance(node, list):
        return [scrub_json(item) for item in node]
    return node


def scrub_text(text: str) -> str:
    """Best-effort redaction for bodies that are not valid JSON."""
    if not text:
        return text
    text = re.sub(
        r'((?i:token|api_key|apikey|password|pw)"?\s*[:=]\s*"?)([^"&,\s}]+)',
        lambda m: m.group(1) + PLACEHOLDER,
        text,
    )
    return text


def scrub_body(body: dict) -> dict:
    text = body.get("text")
    if not text:
        return {"mimeType": body.get("mimeType", ""), "text": ""}
    try:
        decoded = json.loads(text)
    except (json.JSONDecodeError, TypeError):
        return {"mimeType": body.get("mimeType", ""), "text": scrub_text(text)}
    return {
        "mimeType": body.get("mimeType", "application/json"),
        "json": scrub_json(decoded),
    }


def scrub_headers(headers: list[dict]) -> dict[str, str]:
    out = {}
    for header in headers:
        name = header.get("name", "")
        if name.lower() in SENSITIVE_HEADERS:
            out[name] = PLACEHOLDER
        else:
            out[name] = header.get("value", "")
    return out


def scrub_query(url: str) -> tuple[str, dict[str, str]]:
    parts = urlsplit(url)
    params = {}
    for key, value in parse_qsl(parts.query, keep_blank_values=True):
        params[key] = PLACEHOLDER if key.lower() in SENSITIVE_QUERY else value
    return parts.path, params


def slugify(method: str, path: str) -> str:
    """Stable, filesystem-safe name for a route.

    GUID-ish and numeric path segments collapse to {id} so that repeated calls
    against different items land in one fixture directory.
    """
    segments = []
    for segment in path.strip("/").split("/"):
        if not segment:
            continue
        if re.fullmatch(r"[0-9a-fA-F]{32}", segment) or re.fullmatch(
            r"[0-9a-fA-F-]{36}", segment
        ):
            segments.append("{id}")
        elif segment.isdigit():
            segments.append("{n}")
        else:
            segments.append(segment)
    slug = "_".join(segments) or "root"
    slug = re.sub(r"[^A-Za-z0-9{}_.-]", "-", slug)
    return f"{method.upper()}_{slug}"


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__)
        return 2

    har_path = pathlib.Path(sys.argv[1])
    out_root = pathlib.Path(sys.argv[2])

    if not har_path.is_file():
        print(f"no such capture: {har_path}", file=sys.stderr)
        return 1

    har = json.loads(har_path.read_text(encoding="utf-8"))
    entries = har.get("log", {}).get("entries", [])
    if not entries:
        print("capture contains no entries", file=sys.stderr)
        return 1

    out_root.mkdir(parents=True, exist_ok=True)
    seen: dict[str, int] = {}
    order = 0

    for entry in entries:
        request = entry.get("request", {})
        response = entry.get("response", {})
        method = request.get("method", "GET")
        path, params = scrub_query(request.get("url", ""))

        # Skip the media stream itself; the body is enormous and uninteresting.
        # The request line and headers are what matter for range behaviour.
        is_stream = "/videos/" in path.lower() and "stream" in path.lower()

        slug = slugify(method, path)
        order += 1
        index = seen.get(slug, 0)
        seen[slug] = index + 1

        directory = out_root / slug
        directory.mkdir(parents=True, exist_ok=True)

        fixture = {
            "call_order": order,
            "request": {
                "method": method,
                "path": path,
                "query": params,
                "headers": scrub_headers(request.get("headers", [])),
                "body": scrub_body(request.get("postData", {})),
            },
            "response": {
                "status": response.get("status"),
                "headers": scrub_headers(response.get("headers", [])),
                "body": (
                    {"note": "stream body omitted"}
                    if is_stream
                    else scrub_body(response.get("content", {}))
                ),
            },
        }

        target = directory / f"{index:02d}.json"
        target.write_text(
            json.dumps(fixture, indent=2, sort_keys=False) + "\n", encoding="utf-8"
        )

    print(f"wrote {order} fixtures across {len(seen)} routes -> {out_root}")
    print("\nroutes in first-call order:")
    for slug in seen:
        print(f"  {slug}  ({seen[slug]} call(s))")
    print(
        "\nReview these by hand before committing. Automated redaction is a "
        "safety net, not a guarantee."
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
