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
# Proxy/CDN and server-timing headers: artifacts of the environment, not part
# of the Jellyfin wire contract. Dropped so fixtures describe the protocol.
DROP_HEADERS = {
    "cf-ray", "cdn-loop", "cf-ipcountry", "cf-visitor", "cf-connecting-ip",
    "x-forwarded-for", "x-forwarded-proto", "x-forwarded-scheme",
    "x-forwarded-host", "x-real-ip", "x-response-time-ms", "date",
}

# Headers replaced wholesale. These carry a bare secret and no structure worth
# preserving.
SENSITIVE_HEADERS = {
    "x-mediabrowser-token",
    "x-emby-token",
    "cookie",
    "set-cookie",
}

# Headers whose STRUCTURE is part of the wire contract and must survive
# redaction. See scrub_auth_header.
STRUCTURED_AUTH_HEADERS = {
    "authorization",
    "x-emby-authorization",
}

# Parameters inside a structured authorization header that hold a secret.
AUTH_SECRET_PARAMS = {"token"}

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
        if name.lower() in DROP_HEADERS:
            continue
        low = name.lower()
        if low in SENSITIVE_HEADERS:
            out[name] = PLACEHOLDER
        elif low in STRUCTURED_AUTH_HEADERS:
            out[name] = scrub_auth_header(header.get("value", ""))
        else:
            out[name] = header.get("value", "")
    return out


# MediaBrowser authorization grammar: a scheme, then comma-separated
# key="value" pairs.  Token="..." holds the secret; everything else is the
# client identifying itself.
_AUTH_PARAM = re.compile(r'(?P<key>[A-Za-z_][A-Za-z0-9_-]*)\s*=\s*"(?P<value>[^"]*)"')


def scrub_auth_header(value: str) -> str:
    """Redact the secret in an authorization header, keeping its structure.

    Replacing the whole value destroys the thing the fixture exists to
    document.  Reelix parses this header — the scheme, the parameter names,
    the quoting, the spacing — and a fixture reading only "REDACTED" cannot
    validate any of that.  It records that a header was present and nothing
    about its shape, which is precisely the part a compatibility layer has to
    get right.

    So Token is replaced, DeviceId is pseudonymised through the same table the
    JSON bodies use (a device keeps one fake id across the capture), and the
    rest — scheme, Client, Device, Version — is preserved verbatim.

    FAILS CLOSED.  Anything that does not parse as this grammar is replaced
    wholesale: an unrecognised authorization header may hold a secret in a
    form this function does not understand, and emitting it verbatim to be
    committed to a public repository is not a risk worth taking to keep a
    fixture prettier.
    """
    if not value:
        return value

    scheme, _, params = value.partition(" ")
    if not params.strip():
        return PLACEHOLDER

    matches = list(_AUTH_PARAM.finditer(params))
    if not matches:
        return PLACEHOLDER

    # Every non-whitespace, non-separator character must belong to a matched
    # parameter. Anything left over is unparsed input that could be a secret.
    covered = [False] * len(params)
    for match in matches:
        for i in range(match.start(), match.end()):
            covered[i] = True
    for i, char in enumerate(params):
        if not covered[i] and char not in ", \t":
            return PLACEHOLDER

    parts = []
    for match in matches:
        key, raw = match.group("key"), match.group("value")
        low = key.lower()
        if low in AUTH_SECRET_PARAMS:
            parts.append(f'{key}="{PLACEHOLDER}"')
        elif low in PSEUDONYM_KEYS:
            parts.append(f'{key}="{pseudonym(raw)}"')
        else:
            parts.append(f'{key}="{raw}"')

    return f"{scheme} " + ", ".join(parts)


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



_JELLYFIN_PREFIXES = (
    "/system", "/users", "/useritems", "/userviews", "/userimage",
    "/items", "/videos", "/sessions", "/socket", "/quickconnect",
    "/displaypreferences", "/livetv", "/shows", "/mediasegments",
    "/artists", "/genres", "/persons", "/playlists", "/branding",
    "/localization", "/plugins", "/library", "/audio",
)


def _is_jellyfin_route(path: str) -> bool:
    low = path.lower()
    return low == "/" or any(low.startswith(p) for p in _JELLYFIN_PREFIXES)


# Cases the authorization redactor must get right. Kept beside the function
# rather than in a separate file: this script is run by hand, and a self-test
# nobody can find is a self-test nobody runs. `python3 redact.py --selftest`.
_AUTH_SELFTEST = [
    # (input, expected)
    (
        'MediaBrowser Client="Jellyfin Web", Device="Chrome", '
        'DeviceId="realdevice", Version="10.11.8", Token="SECRETVALUE"',
        'MediaBrowser Client="Jellyfin Web", Device="Chrome", '
        'DeviceId="device-001", Version="10.11.8", Token="REDACTED"',
    ),
    # No token: nothing secret, everything preserved.
    (
        'MediaBrowser Client="Wholphin", Device="SK1", DeviceId="realdevice", Version="1.0"',
        'MediaBrowser Client="Wholphin", Device="SK1", DeviceId="device-001", Version="1.0"',
    ),
    # Order is not assumed.
    ('MediaBrowser Token="SECRETVALUE", Client="A"',
     'MediaBrowser Token="REDACTED", Client="A"'),
    # Fail closed: none of these parse, so none is emitted.
    ("Bearer eyJhbGciOi.SECRETVALUE.sig", PLACEHOLDER),
    ('MediaBrowser Client=Unquoted, Token="x"', PLACEHOLDER),
    ("MediaBrowser", PLACEHOLDER),
    ('MediaBrowser Client="A", leftovergarbage, Token="x"', PLACEHOLDER),
]


def selftest() -> int:
    """Check the authorization redactor. Returns a process exit code."""
    global _pseudonyms
    failures = 0

    for value, expected in _AUTH_SELFTEST:
        _pseudonyms = {}
        got = scrub_auth_header(value)
        if got != expected:
            failures += 1
            print(f"FAIL  in : {value}\n      got: {got}\n      want: {expected}")

    # The property that actually matters, checked independently of the table
    # above so a wrong expectation cannot hide a leak.
    _pseudonyms = {}
    for value, _ in _AUTH_SELFTEST:
        if "SECRETVALUE" in scrub_auth_header(value):
            failures += 1
            print(f"FAIL  secret survived redaction: {value}")

    # A device keeps one pseudonym across the capture, or fixtures stop being
    # comparable across requests.
    _pseudonyms = {}
    first = scrub_auth_header('MediaBrowser Client="A", DeviceId="same"')
    second = scrub_auth_header('MediaBrowser Client="B", DeviceId="same"')
    if 'DeviceId="device-001"' not in first or 'DeviceId="device-001"' not in second:
        failures += 1
        print(f"FAIL  device pseudonyms are not stable: {first} / {second}")

    # Through scrub_headers, not just the function. Testing the redactor in
    # isolation leaves the dispatch untested, and a header wired back to a
    # blanket PLACEHOLDER would keep every case above green while destroying
    # exactly the structure this fix exists to preserve.
    _pseudonyms = {}
    headers = scrub_headers([
        {"name": "Authorization",
         "value": 'MediaBrowser Client="Jellyfin Web", DeviceId="d", Token="SECRETVALUE"'},
        {"name": "X-MediaBrowser-Token", "value": "SECRETVALUE"},
        {"name": "Accept", "value": "application/json"},
    ])
    if "SECRETVALUE" in json.dumps(headers):
        failures += 1
        print(f"FAIL  a secret survived scrub_headers: {headers}")
    if "MediaBrowser Client=" not in headers.get("Authorization", ""):
        failures += 1
        print("FAIL  scrub_headers flattened the authorization header; "
              f"structure is the point: {headers.get('Authorization')!r}")
    if headers.get("X-MediaBrowser-Token") != PLACEHOLDER:
        failures += 1
        print("FAIL  a bare-token header was not fully redacted")
    if headers.get("Accept") != "application/json":
        failures += 1
        print("FAIL  an ordinary header was altered")

    _pseudonyms = {}
    if failures:
        print(f"\n{failures} failure(s)")
        return 1

    print(f"selftest passed ({len(_AUTH_SELFTEST)} cases)")
    return 0


def main() -> int:
    if len(sys.argv) == 2 and sys.argv[1] == "--selftest":
        return selftest()

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

        # Skip vulnerability-scanner traffic: 8097 was briefly internet-facing
        # and bots probed it. Anything Jellyfin does not serve is noise.
        if response.get("status") in (404, 400) and not _is_jellyfin_route(path):
            continue

        # jellyfin-web assets: served to the setup wizard in a browser, never
        # to Wholphin. Reelix ships no web client, so these are not fixtures.
        low = path.lower()
        if low.startswith("/web") or low in ("/", "/favicon.ico"):
            continue

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
