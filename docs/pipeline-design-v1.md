# Media Pipeline Design (v1)

*Date: 2026-07-27*


> **Historical.** Written while ARM was still part of the design and kept for
> the reasoning behind separating the stages. hellbox replaced ARM entirely;
> the current design is `phase1-spec.md`.

# Vision

Build a reliable, mostly hands-off home media appliance where ripping,
metadata, transcoding, review, and library management are separate
responsibilities.

The guiding principle:

> Never let a failed transcode require reripping a disc.

------------------------------------------------------------------------

# High-Level Architecture

``` text
DVD / Blu-ray
      │
      ▼
┌─────────────────────┐
│ Automatic Ripping   │
│ Machine (ARM)       │
│ - Detect disc       │
│ - Rip with MakeMKV  │
│ - NO metadata logic │
│ - NO permanent      │
│   transcoding       │
└─────────┬───────────┘
          │
          ▼
Raw MKV Staging
(/srv/media/arm/raw)

          │
          ▼
┌─────────────────────┐
│ Import Queue        │
│ - New file detect   │
│ - Generate hashes   │
│ - Store state       │
└─────────┬───────────┘
          │
          ▼
┌─────────────────────┐
│ Metadata Engine     │
│ - TMDB              │
│ - TVDB              │
│ - IMDb              │
│ - Local overrides   │
│ - Confidence score  │
└─────────┬───────────┘
          │
     High confidence?
      │           │
     Yes          No
      │           ▼
      │    Review Queue
      │    - Human fixes
      │    - Episode mapping
      │    - Title overrides
      ▼
┌─────────────────────┐
│ Transcoder          │
│ FFmpeg + Intel      │
│ VAAPI/QSV           │
│ Preserve audio      │
│ Preserve subtitles  │
└─────────┬───────────┘
          │
          ▼
Library Staging

          │
          ▼
Validation
- Duration
- Streams
- Chapters
- Size sanity

          │
          ▼
Move into:

/library/Movies
/library/TV

          │
          ▼
Jellyfin Refresh
```

------------------------------------------------------------------------

# Responsibilities

## ARM

Responsibilities:

-   Detect inserted discs
-   Rip with MakeMKV
-   Produce pristine MKVs

Should NOT own:

-   Metadata
-   Episode detection
-   Final naming
-   Library organization
-   Permanent transcoding

Recommended configuration during development:

``` yaml
SKIP_TRANSCODE: true
DELRAWFILES: false
```

------------------------------------------------------------------------

# Metadata Service

Written in Go.

Stores every title in PostgreSQL.

Tracks:

-   disc
-   source file
-   title number
-   confidence
-   TMDB id
-   TVDB id
-   overrides
-   retry count
-   processing state

State machine:

    DISC_DETECTED
    ↓

    RIPPED

    ↓

    IDENTIFIED

    ↓

    TRANSCODED

    ↓

    VALIDATED

    ↓

    IMPORTED

    ↓

    COMPLETE

------------------------------------------------------------------------

# Transcoder

Dedicated service.

Not ARM.

Primary engine:

FFmpeg

Preferred hardware:

Intel VAAPI

Future options:

-   Intel QSV
-   NVIDIA NVENC
-   AMD AMF

Goals:

-   preserve original resolution
-   preserve frame rate
-   preserve chapters
-   copy audio when practical
-   preserve subtitles
-   configurable quality profiles

------------------------------------------------------------------------

# Review Queue

Only invoked when confidence is low.

Examples:

-   TV episode ordering ambiguous
-   Bonus features
-   Concert discs
-   Multi-version films
-   Anime specials

Human decisions become persistent overrides.

------------------------------------------------------------------------

# File Layout

    /srv/media/

        arm/
            raw/
            transcode/
            completed/

        importer/
            queue/
            failed/
            logs/

        library/
            Movies/
            TV/

------------------------------------------------------------------------

# Future UI

Web dashboard:

-   Current rip
-   Queue
-   Metadata confidence
-   Preview artwork
-   Compare candidate matches
-   Retry transcode
-   Retry metadata
-   Manual rename
-   Jellyfin refresh

------------------------------------------------------------------------

# Why This Design

Separating concerns provides:

-   rerun transcodes without reripping
-   swap encoders freely
-   easier debugging
-   independent metadata improvements
-   reproducible pipeline
-   resilient automation
-   future distributed processing

ARM becomes a ripping appliance.

The Go application becomes the orchestration layer.

FFmpeg becomes the media engine.

Jellyfin becomes only the presentation layer.

This separation minimizes coupling and makes every stage independently
testable and replaceable.
