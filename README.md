# Juno Highlight Scanner

Find eliminations credited to you as Juno in Overwatch recordings and export their timestamps to CSV. The scanner runs locally. It uses FFmpeg to read the video, NVIDIA hardware decoding when available, and Go to inspect the top-right kill feed.

**For hours of raw footage, start with keyframes.** Use an interval scan when you want a closer pass for short-lived entries.

## Windows quick start

1. Download the Windows x64 ZIP from [Releases](../../releases/latest) and extract it.
2. Install FFmpeg and put both `ffmpeg.exe` and `ffprobe.exe` on PATH. Run `ffmpeg -version` and `ffprobe -version` to check. [FFmpeg downloads](https://ffmpeg.org/download.html)
3. Keep the included `juno-reference.png` and `juno-player.txt` next to the executable. They contain the original calibration and enable OCR fallback. For a different username or HUD, follow [Calibration](#calibration) and update the username in the TXT file, or pass it with `-player YOURNAME`.

Open PowerShell in the extracted folder and scan a long recording with keyframes.

```powershell
.\juno-scanner.exe -keyframes -out .\keyframe-kills.csv 'D:\Recordings\gameplay.mkv'
```

You can also scan a folder of Replay clips concurrently.

```powershell
.\juno-scanner.exe -keyframes -workers 8 -out .\replay-kills.csv 'D:\Recordings'
```

For regular sampling, specify an interval in seconds.

```powershell
.\juno-scanner.exe -interval 3 -out .\interval-kills.csv 'D:\Recordings\gameplay.mkv'
.\juno-scanner.exe -interval 1 -out .\detailed-kills.csv 'D:\Recordings\gameplay.mkv'
```

You can pass several files or folders in one command. For folder inputs, the scanner looks for video files whose names start with `Replay` directly in that folder. Explicit file inputs may have any name. Use a different `-out` path to keep reports separate. The scanner replaces an existing CSV at the chosen output path when it writes results.

## Calibration

Use a clear, full-size kill-feed row from a 1920×1080 screenshot in which the blue side shows your username and Juno's main portrait. The victim should be on the red side. A frame early in the life of the entry with a clear name works well. You can extract a still with FFmpeg.

```powershell
ffmpeg -ss 00:02:30 -i 'D:\Recordings\gameplay.mkv' -frames:v 1 .\calibration-frame.png
```

Use a pixel-accurate crop tool to create a **38-pixel-tall PNG** of the blue attacker box. Start about two pixels left of the box and one pixel above it. End about five pixels beyond the large Juno portrait. Include the full username and main portrait and leave the smaller assist portraits, arrow, and red box outside the crop as much as possible. The width depends on the name and must be 95–300 pixels. Save the unscaled crop as `juno-reference.png` or specify its path with `-reference`.

Save your visible kill-feed username on a single line in `juno-player.txt` alongside the PNG to enable the OCR fallback. Keep both files alongside future executable updates. If your HUD scale, team colors, or portrait presentation changes substantially, compare results on a known kill and recalibrate if needed.

## Command-line options

Put options before the file and directory arguments.

| Option | Default | What it controls |
| --- | --- | --- |
| `-keyframes` | Off | Samples only at the video's keyframe timestamps. Takes precedence over interval sampling. |
| `-interval SECONDS` | `3` | Sampling interval in seconds for regular scans. Supports decimals down to 0.1 seconds. |
| `-workers N` | `1` | Maximum number of recordings scanned concurrently. Each recording uses one worker. |
| `-decode MODE` | `auto` | `auto` tries CUDA and retries on CPU if video decoding fails. `cuda` requires NVIDIA hardware decoding. `cpu` forces software decoding. |
| `-match MODE` | `auto` | `auto` tries fast template matching and falls back to local OCR when a player name is configured. `ocr` uses the OCR path. `template` uses the calibrated pattern only. |
| `-reference PATH` | `juno-reference.png` | Calibration PNG. The default filename is checked in the current folder, then beside the executable. |
| `-player NAME` | Optional | Visible name for OCR, with letters A–Z and digits 0–9. If omitted, uses `juno-player.txt` beside the calibration PNG, when present. |
| `-out PATH` | `juno-kills.csv` | Destination CSV. The parent folder must already exist. |
| `-h` | | Shows usage and flags. |

## Read the CSV

| Column | Meaning |
| --- | --- |
| `video_file` | Recording filename. |
| `timestamp` | Sample time as hours, minutes, seconds, and milliseconds. |
| `seconds` | Same sample time in seconds, useful for sorting or scripting. |
| `new_kills` | Newly detected qualifying kill-feed rows at that sample. |
| `visible_juno_kills` | All qualifying rows visible in the sample, including lingering rows. |
| `multi_kill` | `true` if there was a new kill while at least two qualifying rows were visible together. |
| `video_path` | Full source path. |

Seek a few seconds before the reported timestamp for the lead-up. The scanner compares victim lettering relative to the HUD margin and matches visible rows one-to-one. A second simultaneous row can still count if it shows the same victim. The recent match window is six seconds in keyframe mode and four seconds at the default regular interval.

## Benchmarks

Wall-clock measurements on an RTX 3070 with CUDA decoding. The long recording was read from a network share. Keyframe frequency, codec, storage speed, and OCR work can change runtimes and coverage.

### Raw recording, 1:18:39, 19.3 GiB, H.264 1080p60

| Sampling | Frames analyzed | Scan time | Checked detections |
| --- | ---: | ---: | --- |
| Every 3 seconds | 1,573 | 6:28 | 6 distinct kills |
| Keyframes only | 1,225 | 1:22 | The same 6 distinct kills |

Keyframes were roughly four seconds apart in sampled portions. The first keyframe prototype returned one duplicate when a bright background edge showed through the victim box. The matcher now anchors victim lettering to the HUD margin. The corrected full rerun returned six rows. Keyframe mode was about **4.7× as fast** on this recording and saved about **5 minutes**.

### Highlight compilation, 3:43

| Interval | Scan time | Highlight timestamps in the original benchmark |
| --- | ---: | ---: |
| 1 second | 13.6 seconds | 23 |
| 3 seconds | 13.5 seconds | 12 |
| 5 seconds | 13.5 seconds | 10 |

One-second sampling caught a triple visible at 1:40. All three interval runs decoded the intervening frames, so analyzing fewer crops made little difference to runtime. The one-second retest after the deduplication fix produced 22 timestamp rows.

### Replay batches

A separate run scanned 91 Replay clips from the network share in 6:54 at one-second intervals with eight workers. GPU memory was about 4 GiB in a spot check, compared with about 7.7 GiB during a 32-worker run on a different batch. Eight workers are a practical starting point for similar batch work on an 8 GiB GPU. The decoder was already near 100% during the long single-file interval run.

## Detection limits

Keyframe mode can miss an entry that appears and disappears between keyframes. Interval scans can also miss entries between samples. The benchmarks compare detections on the tested files. The calibration targets blue attackers and red victims. Spectator views with your side in red, major HUD changes, and unreadable lettering may yield fewer detections.

## Build from source

With Go 1.22 or later and FFmpeg available:

```powershell
go test ./...
go build -trimpath -o juno-scanner.exe .
```

The source checkout includes the original calibration PNG and username TXT. If you move the built executable out of the checkout, copy both files beside it.

The executable includes the English Tesseract model and uses [gogosseract](https://github.com/Danlock/gogosseract) and [Wazero](https://github.com/tetratelabs/wazero) to run OCR locally. See [third-party notices](THIRD_PARTY_NOTICES.md) for licensing.
