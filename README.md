

# rabbitRPA

### (Probably) the world's first one file RPA tool impremented by Golang!

![icon](https://github.com/yasutakatou/rabbitRPA/blob/pics/icon.png)

lovely Lepus brachyurus!<br>

### So far, it only works on Windows.

# demo

![demo](https://github.com/yasutakatou/rabbitRPA/blob/pics/rabbitRPA.gif)

# solution

You try to automation on windows gui operation, you might use RPA tool. 
after, you will setup this tool requirement for execute.
install, config file customize, and depend tool and more.
### I wonder why, RPA tools are too fat.
I want do old installer to automate, only once.
But, RPA set up is a pain in the ass. I think needs, light weight RPA tool.

# features

 - **one binary (it's not perfect)**
 - light weight
 - very simple use
 - (Of course) free!

# structure

1. one execute file include all binary and dll files.
2. when execute this, extract execute file for RPA, and OpenCV dll files.
3. and then, execute RPA binary depend for OpenCV, Your operation recorded or replayed!

# algorithm

this tool have two functions roughly. It's Record or Replay.<br>

## Record

When no option, tool is Record mode.<br>

```
rabbitRPA.exe
```

tool executed and create require files, after follow message.


```
 - - recording start! you want to end this mode, key press ascii code (27) - -
```

until input exit key code, your operation recorded.<br>
(default exit key code is **27[Escape Key]**.)<br>

After input exit key code, your operation is recorded tsv file and captures.<br>

tsv file include target window title, move value mouse, click position.<br>
captures include all screen, and target window capture.<br>
*tool calculates the capture's difference.*<br>
*Therefore, If target windows moved another position, that position adjust.*<br>

<br>

## Replay

Replay the operation using the file you just used. set you options. <br>

```
rabbitRPA.exe -replay -import=output.tsv
```

*"-replay"* is replay mode option.<br>
*"-import"* is exported tsv file for record mode.<br>

### your record will replay!

[See here for other options.](https://github.com/yasutakatou/rabbitRPA#options)

# installation

download binary from [release page](https://github.com/yasutakatou/rabbitRPA/releases).<br>
save binary file, copy to entryed execute path directory.<br>

## another

this tool depend gocv and statik.<br>

[GoCV](https://github.com/hybridgroup/gocv)<br>
[statik](https://github.com/rakyll/statik)

step1. you install gocv(and opencv)

See below.

https://github.com/hybridgroup/gocv#windows

```
win_build_opencv.cmd
```

step2. "statik" set up

```
go get github.com/rakyll/statik
```

step3. clone this repository

```
git clone https://github.com/yasutakatou/rabbitRPA
cd rabbitRPA
```

step4. copy opencv dlls and converted by statik

Copy the dll from the opencv folder. (total 11 files)<br>
ex) **C:\opencv\build\install\x64\mingw\bin** to **.\rabbitRPA**<br>

```
libopencv_calib3d430.dll
libopencv_core430.dll
libopencv_dnn430.dll
libopencv_features2d430.dll
libopencv_flann430.dll
libopencv_highgui430.dll
libopencv_imgcodecs430.dll
libopencv_imgproc430.dll
libopencv_objdetect430.dll
libopencv_video430.dll
libopencv_videoio430.dll
```

Build the RPA worker (Windows, cgo + OpenCV required).

```
go build ./cmd/do
```

Place `do.exe` and the OpenCV DLLs in `internal/assets/dist/` so the launcher can embed them with `go:embed` (no separate statik step).

Finally, package the launcher.

```
go build ./cmd/rabbitRPA
```

## uninstall

```
delete that binarys.
```

del or rm command. *(it's simple!)*

# options

*note) this options give to RPA binary(do.exe) as is. therefore, do.exe's options same too.*

|option name|default value|detail|
|:---|:---|:---|
-list|false|listing window titiles and exit<br>use to search target window title.|
-replay|false|replay mode (true is enable)|
-record|true|recording mode (true is enable)<br>default is on(recording mode)|
-export|output.tsv|export file name<br>**If exists same file name, it's overwriten**.|
-import|input.tsv|import file name|
-debug|false|debug mode (true is enable)|
-threshold|0.1|same window threshold<br>**The lower the value, the more, select large difference**.<br>If screen size is more larger when it's replay, try value lower.|
-move|50|mouse move record threshold<br>record the mouse move par this value.<br>When value is lower, recording often.|
-try|10|error and try counter<br>**In case of wait next screen a while, set value larger**.|
-wait|250|loop wait Millisecond|
-exitCode|27|recording mode to exit ascii key code<br>ascii code, [please refer this site](http://www9.plala.or.jp/sgwr-t/c_sub/ascii.html).|
-tmpDir|tmp|temporary directory name<br>save captures to this directory.|
-report|replay-report.json|replay only: JSON audit report path (empty string disables it). The report records every step with its original TSV line number.|
-keepTmp|false|replay only: keep the per-run scratch directory for inspection instead of removing it.|

# transactional replay

Replay is an all-or-nothing transaction with a verifiable audit trail:

 - **Strict preflight before any desktop action.** The TSV must exist and be non-empty; every line must have exactly four tab fields; device type, semicolon field counts, integer offsets/buttons and every referenced capture file are validated up front. When the platform can enumerate windows, every target window title is resolved before execution as well. All preflight errors are reported with their **1-based TSV line number**.
 - **No silent skipping.** A missing or ambiguous target window, a failed screenshot, a missing template, a template larger than the screenshot or a match that stays below `-threshold` after all `-try` attempts is a hard failure with a non-zero process exit code. The original substring window matching and Pre→Aft fallback semantics are retained, but several equally sized matching windows now fail with an ambiguity error instead of silently picking one.
 - **Per-step results with line numbers.** Each executed step produces a JSON record (`line`, `device`, `pre`, `aft`, `params`, `status`, `attempts`, `actionX/actionY`, timestamps, error) in `-report`. Exit codes: `0` success, `1` replay/preflight failure, `2` interrupted (Ctrl+C/SIGTERM), `3` flag usage error, `4` platform unsupported.
 - **Resources scoped to one invocation.** Each replay creates a unique `tmp/replay-run-<timestamp>-<id>/` scratch directory for its screenshots and removes it on every exit path (success, failure, panic or interrupt). Recorded captures, the TSV and any pre-existing tmp content are never deleted; use `-keepTmp` to inspect a run's screenshots.
 - **argv, never a shell string.** The launcher starts `do(.exe)` via `exec` with one argv element per argument — paths containing spaces or quotes cannot be re-parsed by `cmd /C` — and the child's exit code is propagated instead of being forced to zero.
 - **No shared state between runs.** Configuration and per-run state live in an engine instance, not package globals; repeating a replay cannot read another run's temporary files.

## acceptance without a real desktop

Replay can be exercised fully off-Windows with a scripted adapter (no clicks, no screen access):

```
RABBITRPA_FAKE=1 RABBITRPA_FAKE_WINDOWS="Untitled - Notepad|Calculator" \
  do -replay -import=input.tsv -report=report.json
```

`RABBITRPA_FAKE_WINDOWS` is a `|`-separated fake window list. Go tests (`go test ./...`) cover the strict TSV parser, missing/ambiguous windows, capture and template failures, threshold retries, interruption, cleanup isolation, launcher argv integrity and exit-code propagation.

# build

The repository now builds as normal Go packages (the old root-level two-main-file layout moved under `cmd/`):

```
go build ./cmd/do           # RPA worker (record + replay)
go build ./cmd/rabbitRPA    # single-binary launcher
```

Native record/replay still requires Windows with cgo and OpenCV (GoCV). A `CGO_ENABLED=0` Windows build type-checks every package and returns an explicit error if desktop automation is invoked without OpenCV. The launcher embeds `do.exe`/OpenCV payloads from `internal/assets/dist/` via `go:embed` (placeholder files keep the checkout buildable; drop the real payloads there for release builds).


# Problem

 - If exists same title window, replay now fails explicitly.

Window search still uses partial (bidirectional substring) title matching, because some tools change their window title while focused. When more than one non-zero-sized window matches, the replay transaction aborts with an ambiguity error (carrying the TSV line number) instead of silently operating on the smaller window handle. Disambiguate by using a more specific title in the TSV.

 - Why double click don't record?

This tool depend golang module, and that module detect click count only.<br>
I mean, When double click want to enable, must detect how long it takes to double click.<br>
Each PC's specs are different. It's difficult!<br>

# copyright

(Probably) the world's first one file RPA tool impremented by Golang!<br>
Copyright (c) 2020 yasutakatou

# license

3-clause BSD License<br>
 and<br>
Apache License Version 2.0<br>
