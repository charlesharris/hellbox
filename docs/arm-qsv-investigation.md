# ARM / Intel QSV Investigation Notes

*Date: 2026-07-27*


> **Historical.** The investigation that led to dropping ARM and HandBrake QSV.
> Kept as the evidence behind the decision; not a description of the current
> system.

## Executive Summary

The Intel GPU and VA-API stack are functioning correctly inside the ARM
container, but the bundled HandBrake/QSV stack is **not currently
capable of performing hardware H.264/H.265 encodes successfully**.

Current recommendation:

-   Use ARM primarily for **disc detection + MakeMKV ripping**.
-   Set:
    -   `SKIP_TRANSCODE: true`
    -   `DELRAWFILES: false`
-   Build a separate transcoding stage (likely FFmpeg + VAAPI/QSV) for
    long-term automation.

------------------------------------------------------------------------

# Environment

## Host

-   Ubuntu Server
-   Intel N100
-   Intel Quick Sync capable
-   Docker Compose
-   ARM container
-   Jellyfin container

GPU devices:

    /dev/dri/card1
    /dev/dri/renderD128

------------------------------------------------------------------------

# ARM Configuration

Current settings:

``` yaml
SKIP_TRANSCODE: false
DELRAWFILES: true
MAX_CONCURRENT_TRANSCODES: 0
MAINFEATURE: false

HB_PRESET_DVD: "HQ 720p30 Surround"
HB_PRESET_BD: "HQ 1080p30 Surround"
```

These presets are not ideal for PAL TV DVDs.

------------------------------------------------------------------------

# Initial Symptoms

Still Game DVD required roughly:

-   \~20 minutes per episode
-   \~2--3 hours for one disc

ARM classified most episodes as:

    extras/

instead of TV episodes.

------------------------------------------------------------------------

# Intel GPU Investigation

## Container access

Verified:

-   `/dev/dri` correctly mounted
-   Intel driver loads
-   `vainfo` succeeds

Output:

    Driver:
    Intel iHD

    va_openDriver() returns 0

Hardware capabilities include:

-   H.264 encode
-   HEVC encode
-   HEVC Main10 encode
-   VP9 encode
-   AV1 decode

This confirms:

✅ GPU is healthy

------------------------------------------------------------------------

# HandBrake Investigation

Initial encoder list:

No QSV encoders.

After installing/updating Intel runtime:

    qsv_h264

appeared.

However:

    qsv_h265

does not exist in this build.

------------------------------------------------------------------------

# QSV Benchmark

Command executed:

``` bash
HandBrakeCLI \
  -e qsv_h264 \
  -q 20 \
  --encoder-preset balanced
```

HandBrake correctly detects:

-   Intel Quick Sync
-   Intel Media SDK
-   H.264 hardware encoder

It reports:

    H.264 encoder: yes
    H.265 encoder: no

Encoding begins, then immediately fails:

    ERROR: hwaccel: failed to create hwdevice
    ERROR: Failure to initialise thread 'FFMPEG encoder'

Additional warning:

    libva doesn't support retrieving the device information
    Please consider upgrading libva to VA-API 1.15

Result:

No frames encoded.

------------------------------------------------------------------------

# Conclusions

## Working

✔ Intel GPU

✔ Intel media driver

✔ VAAPI

✔ MakeMKV

✔ ARM ripping

✔ HandBrake software encoding

------------------------------------------------------------------------

## Broken

✘ HandBrake hardware encoding

Specifically:

HandBrake can enumerate QSV.

HandBrake cannot actually create the hardware encoder device.

Likely cause:

Version mismatch between:

-   HandBrake
-   FFmpeg
-   libva
-   Intel Media SDK/libmfx/libvpl

------------------------------------------------------------------------

# Quality observations

Current ARM preset:

    HQ 720p30 Surround

Problems:

-   unnecessary scaling
-   tuned for movies
-   poor fit for PAL TV DVDs

Resulting episodes:

\~900 MB--1 GB each

Likely much larger than necessary.

------------------------------------------------------------------------

# Recommended Future Architecture

    DVD

    ↓

    ARM

    ↓

    MakeMKV

    ↓

    Raw MKVs (keep)

    ↓

    Importer / Queue

    ↓

    FFmpeg (Intel VAAPI/QSV)

    ↓

    Metadata

    ↓

    Jellyfin Library

Advantages:

-   preserve originals
-   retry transcodes
-   independent metadata
-   independent transcoder
-   TV episode mapping
-   easier debugging

------------------------------------------------------------------------

# Immediate Configuration Changes

Recommended:

``` yaml
SKIP_TRANSCODE: true
DELRAWFILES: false
```

This allows:

Disc

↓

Raw MKVs

↓

Experiment with transcode settings

without rereading discs.

------------------------------------------------------------------------

# Future Work

1.  Preserve raw MKVs.
2.  Build dedicated FFmpeg transcoder container.
3.  Benchmark VAAPI H.264.
4.  Benchmark VAAPI HEVC.
5.  Replace ARM metadata handling with Go importer.
6.  Eventually automate complete pipeline.

------------------------------------------------------------------------

# Key Findings

-   Hardware is NOT the problem.
-   GPU passthrough is correct.
-   Intel driver works.
-   VAAPI works.
-   HandBrake QSV implementation is currently the limiting factor.
-   The long-term design of separating ripping from transcoding is
    reinforced by these findings.
