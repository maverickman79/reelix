# Reelix — Project Constitution

## Project Purpose

Reelix is a modern, Docker-first media server designed to provide a
significantly improved server-management experience while maintaining
compatibility with existing Jellyfin playback clients.

The server provides its own backend architecture, database model, administration
interface, library engine, metadata system, user-management system, playback
engine, and transcoding subsystem.

Existing Jellyfin clients should eventually be able to connect to Reelix using a
Jellyfin-compatible API without requiring modifications to those clients.

This project is **not a fork of Jellyfin**.

Do not copy Jellyfin's internal architecture merely for compatibility.

Jellyfin compatibility is an external protocol requirement, not an internal
architectural requirement.

---

## Identity and Licensing

- **Name:** Reelix
- **Go module path:** `github.com/maverickman79/reelix`
- **License:** AGPL-3.0
- **Container image:** `reelix/server`

The AGPL choice is deliberate: it matches the norms of the self-hosted media
server ecosystem and closes the network-service loophole. It is *not* inherited
from Jellyfin — Reelix shares no code with Jellyfin and is not bound by
Jellyfin's GPL-2.0.

That independence is only real if it is maintained. See "Clean-room rule" below.

Naming is sticky. The module path, image name, and database identifiers should
not change after 0.1.0 without a deliberate migration plan.

---

## Clean-room rule

**Permitted sources of Jellyfin compatibility knowledge:**

- Observed HTTP traffic between a real client and a real Jellyfin server
- Jellyfin's published OpenAPI specification
- Jellyfin's public API documentation
- Third-party client source code (Wholphin, Jellyfin Kotlin SDK, etc.), read to
  understand what a *client* sends and expects

**Never permitted:**

- Copying, pasting, or transliterating Jellyfin server source code
- Porting Jellyfin's C# implementations into Go
- Vendoring Jellyfin code in any form

Compatibility is derived from the wire, never from the source.

---

## Core Product Goals

Reelix should eventually provide:

* Jellyfin-compatible client connectivity
* Modern web-based administration
* Improved user and group management
* Role-based permissions
* Better library creation and configuration
* Better metadata management
* Multiple configurable Continue Watching experiences where technically possible
* Improved home-screen and library organization
* Explicit metadata provenance
* Background job management
* Detailed playback/session visibility
* Hardware-accelerated transcoding
* Intel Quick Sync support
* NVIDIA NVENC/NVDEC support
* AMD VA-API support
* CPU software-transcoding fallback
* Docker-first deployment
* PostgreSQL persistence
* Extensible metadata providers
* Extensible architecture for future plugins and distributed workers

---

## Technology Direction

Unless explicitly changed by the project owner, use:

### Backend

* Go
* PostgreSQL
* FFmpeg
* REST APIs
* WebSocket support where appropriate

Redis may be introduced when there is a demonstrated architectural need for:
caching, distributed jobs, session coordination, queues, rate limiting, pub/sub,
or distributed workers.

Do not introduce Redis merely because it might be useful someday.

### FFmpeg

Reelix does **not** build its own FFmpeg. Use the `jellyfin-ffmpeg7` binaries
in the container image.

This is a pragmatic choice, not a compatibility one: those builds already carry
the QSV, NVENC, and VA-API patches and driver plumbing that make hardware
acceleration work reliably across the hardware Reelix targets. Rebuilding that
work has no upside.

FFmpeg is invoked as a **subprocess**. It is never linked, never cgo-bound.
The binaries are separately licensed and separately distributed; shelling out
keeps that boundary clean.

Both `ffmpeg` and `ffprobe` paths must be configurable, with sane defaults for
the container image.

### Administration Frontend

Use a modern web application architecture.

Frontend framework selection should be proposed separately and approved before
implementation.

The administration UI must consume the project's native API.

The administration UI must never directly depend on the Jellyfin compatibility
API.

### Deployment

Docker is the canonical deployment method.

The primary supported deployment path is:

```text
docker compose up -d
```

Development outside Docker is allowed when useful, but all production
functionality must remain Docker-compatible.

---

## Architectural Principle #1

### Native architecture first. Compatibility second.

The server must have its own internal domain models and services.

Conceptually:

```text
Jellyfin Client
      │
      ▼
Jellyfin Compatibility API
      │
      ▼
Compatibility Translation Layer
      │
      ▼
Native Application Services
      │
      ▼
Domain Models
      │
      ▼
PostgreSQL
```

Never structure native application services around Jellyfin DTOs.

Never use Jellyfin DTOs as database entities.

Never allow Jellyfin-specific naming or behavior to leak into unrelated core
services unless absolutely necessary.

---

## Architectural Principle #2

### Two API surfaces

Reelix exposes two logical APIs.

#### Native API

```text
/api/v1/*
```

Used by: administration interface, future first-party tools, automation,
integrations, server management.

This API represents the project's real architecture.

#### Jellyfin Compatibility API

Jellyfin-compatible routes such as:

```text
/System/Info
/Users
/Items
/Sessions
/Videos
```

This API exists exclusively to support Jellyfin-compatible clients.

Requests must be translated into native services.

All compatibility handlers live under a single package boundary
(`internal/compat/jellyfin`). Nothing outside that package imports its types.

---

## Architectural Principle #3

### Small services over god objects

Avoid large manager types or packages that own unrelated responsibilities.

Prefer explicit services such as:

```text
LibraryService
LibraryScanner
MediaRepository
UserService
AuthenticationService
PlaybackService
PlaybackDecisionEngine
TranscodeService
MetadataService
JobService
SessionService
```

Each component should have a narrow responsibility.

---

## Architectural Principle #4

### Database models are not API models

Maintain separation between:

```text
Database Entity
Domain Model
Native API DTO
Jellyfin Compatibility DTO
```

Do not expose database entities directly through HTTP APIs.

---

## Architectural Principle #5

### Background work is observable

Long-running operations must eventually run as jobs: library scans, metadata
refreshes, image downloads, media probing, chapter generation, trickplay
generation, subtitle scans, scheduled maintenance.

Jobs should eventually support the states:

```text
queued
running
completed
failed
cancelled
```

Where appropriate: progress, total work, current item, start time, completion
time, error information, logs.

Do not block HTTP requests while performing large library operations.

---

## Library Architecture

Libraries represent logical collections of media.

A library may eventually include multiple filesystem locations.

```text
Movies
├── /media/movies
├── /media/movies-4k
└── /media/foreign-movies
```

Library configuration should eventually support: media type, multiple paths,
metadata preferences, image preferences, language, region, scanner settings,
filename rules, embedded metadata rules, monitoring behavior, scheduled scans,
analysis settings, per-library metadata providers.

Do not hard-code assumptions that one library equals one filesystem folder.

---

## Media Domain

The internal media model is independent of Jellyfin.

Initial media concepts:

```text
MediaItem
Movie
Series
Season
Episode
MediaFile
MediaStream
Person
Collection
Artwork
ExternalIdentifier
PlaybackState
```

A media item may have one or more physical media files.

Do not assume:

```text
Movie == File
```

Cases requiring multiple files include alternate editions, multiple resolutions,
director's cuts, multi-part media, and different encodes.

---

## Identifiers

Internally, entity IDs are PostgreSQL `uuid`.

The Jellyfin compatibility layer must serialize IDs as **32-character
lowercase hexadecimal with no dashes**. Clients string-compare these values and
use them to build URLs; a dashed UUID will produce failures that look like
missing content rather than malformed IDs.

Conversion happens **only** at the compatibility boundary. Native services and
the native API use standard dashed UUIDs.

---

## External IDs

Metadata identities should be explicit:

```text
TMDB
TVDB
IMDb
MusicBrainz
AniDB
AniList
```

External identifiers are stored separately from provider-specific metadata.
Metadata providers must be able to reference shared identifiers.

---

## Metadata Philosophy

Metadata must eventually support provenance. For every managed field, the system
should know where the value came from.

```text
Title
Value: Dune: Part Two
Source: TMDB
Locked: false
```

Metadata workflows should eventually support: refresh, search, replace, lock,
unlock, provider selection, image selection, field-level editing, source
visibility.

A metadata refresh must not silently overwrite explicitly locked fields.

---

## Metadata Provider Architecture

Metadata providers conform to explicit interfaces rather than being embedded
throughout the library scanner.

Responsibilities:

```text
Search
Identify
FetchMetadata
FetchImages
ResolveExternalIDs
```

Providers must be independently testable.

---

## User Management

Do not recreate Jellyfin's user model simply for compatibility.

The native model should eventually support:

```text
Users
Groups
Roles
Permissions
Library Access
Playback Policies
Device Policies
Remote Access Policies
Download Policies
Administrative Permissions
```

Permissions should be composable. Group-level configuration should reduce
repetitive per-user configuration.

Jellyfin compatibility endpoints translate these capabilities into the closest
compatible Jellyfin representation.

---

## Authentication and the compatibility boundary

Jellyfin clients authenticate in a way that is fiddly and must be handled
explicitly at the compatibility layer.

Credentials arrive as an authorization header — historically
`X-Emby-Authorization`, and as `Authorization: MediaBrowser ...` — carrying
comma-separated `Key="Value"` pairs describing the client, device, device ID,
and version, and optionally the token. Some clients also send the token as a
separate `X-MediaBrowser-Token` header.

Requirements:

- Parse both header names and both token locations.
- Treat parsing as its own tested unit. It has real edge cases: quoted values
  containing commas, inconsistent spacing, missing fields, casing differences.
- Never log the parsed token or the raw header.
- Translate the result into a native session/device record; do not persist the
  Jellyfin-shaped structure.

Native API authentication is independent and must not reuse this scheme.

---

## Playback

Playback is a separate domain from library management.

The playback subsystem evaluates:

```text
MediaFile
DeviceCapabilities
UserPlaybackPolicy
NetworkConditions
RequestedQuality
SubtitleRequirements
AudioRequirements
```

and returns a decision:

```text
DirectPlay
DirectStream
Transcode
Unsupported
```

Do not embed FFmpeg command construction throughout HTTP handlers.

### Streaming correctness

The direct-play endpoint must implement HTTP range requests correctly:
`Accept-Ranges`, `206 Partial Content`, correct `Content-Range`, correct
`Content-Length` for the returned range, and correct handling of open-ended and
multi-range requests.

Seeking is where naive implementations fail, and the failure presents as a
player that stalls or restarts rather than as an HTTP error. Range handling gets
its own tests with explicit byte-offset assertions.

Go's `http.ServeContent` handles this correctly and should be preferred over a
hand-rolled implementation.

---

## Transcoding

FFmpeg is the media-processing engine.

Hardware-specific functionality must be isolated behind explicit abstractions:

```text
TranscodeBackend
├── SoftwareBackend
├── IntelQsvBackend
├── NvidiaBackend
└── AmdVaapiBackend
```

The application should eventually detect available acceleration capabilities.

Priorities:

1. Intel Quick Sync / QSV
2. NVIDIA NVENC / NVDEC
3. AMD VA-API
4. CPU software fallback

Actual backend selection depends on detected hardware and administrator
preference.

Docker configuration provides device access. Application code determines
capabilities and builds FFmpeg pipelines.

---

## Hardware Detection

Hardware detection inspects available devices and capabilities:

```text
/dev/dri
NVIDIA runtime
vainfo
ffmpeg encoders
ffmpeg decoders
GPU identity
driver version
```

Never assume that device presence guarantees codec support. Capability checks
must be explicit.

---

## Security

Security-sensitive information must not be stored in logs.

Never log: passwords, API tokens, authentication headers, session tokens,
database credentials.

Passwords must be securely hashed. Authentication must be implemented using
well-understood security primitives. Do not invent custom cryptography.

---

## Logging

Use structured logging (`log/slog`).

Prefer the fields:

```text
level
timestamp
component
operation
request_id
user_id where appropriate
job_id where appropriate
error
```

Avoid noisy logs that provide no operational value.

---

## Error Handling

Errors must be explicit and actionable. Avoid swallowing errors. Internal errors
should retain enough context for debugging.

External API responses must not leak stack traces, credentials, filesystem
secrets, or database internals.

**Compatibility-layer specific:** a Jellyfin client encountering a `500` will
usually present it as an empty screen with no explanation. Endpoints that a
client polls opportunistically — artwork, user views, display preferences —
must return well-formed empty responses or `404`, never `500`, when the
underlying data does not exist. An unimplemented compatibility route should
return a valid empty shape rather than an error.

---

## Testing

Tests are required for important domain behavior.

Prioritize: parser logic, path handling, library scanning, metadata
normalization, authorization, playback decisions, compatibility translation,
range/streaming behavior, database behavior.

Every bug fix receives a regression test when practical.

Compatibility translation is tested against **recorded fixtures** captured from
a real Jellyfin server. See `docs/compat-capture.md`.

---

## Database Migrations

All persistent schema changes require migrations.

Never require users to manually recreate the database after normal upgrades.

Migrations must be versioned, deterministic, reviewable, and safe to apply
automatically. Destructive migrations require explicit consideration.

---

## Docker Requirements

The application must work without requiring privileged containers. Avoid broad
host access.

Mount media read-only by default:

```text
/media:ro
```

Write access is requested only when a feature explicitly requires modifying
media files.

Persistent state lives outside the application container. At minimum:

```text
/config
/cache
PostgreSQL volume
```

---

## Configuration

Configuration supports environment variables and/or persistent configuration.

Secrets must not be committed into source control. Provide `.env.example` when
environment variables are required.

---

## Versioning

Early development uses semantic-style pre-release versions:

```text
0.0.1
0.0.2
0.1.0
```

Breaking changes are acceptable before 1.0 but must still be documented.

---

## Git Discipline

Prefer small commits that represent understandable units of work.

Do not combine refactors, new features, dependency upgrades, and formatting
rewrites into one unrelated change.

Do not rewrite large working areas of the project unless specifically necessary.

---

## Dependency Rules

Before adding a dependency:

1. Explain the problem it solves.
2. Determine whether the standard library already solves it adequately.
3. Verify that the dependency is actively maintained.
4. Avoid duplicate libraries solving the same problem.
5. Prefer mature dependencies for security-critical functionality.

Do not introduce dependencies merely to save a few lines of code.

Go's standard library covers routing (`net/http` with method patterns), file
serving with range support (`http.ServeContent`), and structured logging
(`log/slog`). Reach for those first.

---

## AI Development Rule

Never respond to a task by attempting to build the entire future product.

The project is built incrementally. Each task has:

```text
Goal
Scope
Non-goals
Implementation
Tests
Completion criteria
```

Prefer a working narrow vertical slice over a broad, partially implemented
system.
