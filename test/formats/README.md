# Format corpora

Each `corpora/<name>.log` is **CRI-format**: every line is

    <RFC3339Nano> <stdout|stderr> <F|P> <the application's log line>

because that is what the `container` operator expects and what a real node writes.
The stream field matters: CRI interleaves stdout and stderr into ONE file, and
that interleaving is itself a bug source (see corpora/streams.log).

`expected/<name>.jsonl` holds one `{"body":…,"severity":…}` object per emitted
record, sorted by (severity, body) — a canonical MULTISET OF PAIRS. Regenerate
deliberately with `go test ./test/formats -bless -run '.*/<name>'`; a diff means
behaviour changed and you must say why.

## Running it

The suite is a Go test module in this directory. It builds the chart's filelog
receiver **in-process** from the CHART's rendered config, using the collector's
own `confmap` — so it parses config exactly as the binary does, and needs `helm`
but not `./_build/fixter-collector`.

    go test ./test/formats                       # everything, ~8s
    go test ./test/formats -run 'TestCatchAll/java'
    go test ./test/formats -run 'TestPresets/spring'
    go test ./test/formats -run 'TestNoMerge/zap-console'
    go test ./test/formats -bless -run '.*/preset-spring'

Note that `go test -run` on a name that matches nothing prints
`warning: no tests to run` and exits 0. The shell harness this replaced exited 2
on an unknown corpus name. CI runs the whole suite, so this only bites an
interactive typo — but read the warning.

## Three passes — do not confuse what they assert

The suite runs corpora through two different rendered configs, and the goldens
mean opposite things. Pass 3 asserts no golden at all:

| pass | test | config | receiver(s) | golden | records |
|---|---|---|---|---|---|
| 1 | `TestCatchAll` | default (`formats: []`) | `file_log/default` (the catch-all) | `expected/<corpus>.jsonl` | structural severity or NONE; `^\s`-recombined (see below) |
| 2 | `TestPresets` | `presets-values.yaml` | one `file_log/<preset>` per preset | `expected/preset-<name>.jsonl` | **CORRECT behaviour, always** |
| 3 | `TestNoMerge` | `presets-values.yaml` | one `file_log/<preset>` per preset | **none — derived** | `records == the line count` of `no-merge/<preset>.log` (see below) |
| 3′ | `TestCatchAllNoMerge` | default (`formats: []`) | `file_log/default` (the catch-all) | **none — derived** | `records == the column-0 line count` of `no-merge/catch-all.log` (see below) |

A pass-1 golden pins what a receiver does when nobody has told it the format:
JSON and glog resolve structurally, everything else gets no severity, and the
catch-all recombines indented continuation lines into the record above via `^\s`
(so a stack trace's frames rejoin, but every line is NOT its own record — see the
catch-all recombine section below). **Never** "fix" a pass-1 golden by widening a
pattern until a severity appears — see below for why that is the bug, not the fix.

A pass-2 golden is a *contract*: a preset knows its format, so every value in it
must be right. If a preset golden ever needs a wrong value to go green, the
preset is broken — fix the preset, not the golden. `Unspecified(0)` in a preset
golden is legitimate and means only one thing: that line is **not** that format
(the `trace-service` decoy in `presets/spring.log`, Postgres' `STATEMENT:`
detail line), so the preset declines to name a level. Declining is a
degradation; naming the WRONG level is a lie, and a lie is invisible to
alerting. No preset golden may contain a wrong level.

Pass 2 exists because pass 1 renders **no preset receiver at all** — with
`formats: []` there is no `file_log/<preset>` anywhere in the config. Pass 1 was
12/12 green while the `logfmt` preset shipped every `ERROR` as `Trace(1)` and
the `spring` preset lost severity on every Spring Boot 2.x line. Manual probes
were the only thing standing between a rotted preset and silently-wrong
severity. Pass 2 is driven off the CHART's preset list, so a new preset with no
corpus/golden FAILS rather than shipping untested.

## Pass 3 — the no-merge guard (`no-merge/`)

Pass 3 exists because the SAME bug has now been found **three times**, and every
time by someone happening to probe the right config knob — never by reading the
regex, and never by a golden going red.

A preset joins on `continuationRegex`: a line matching it JOINS the record above,
anything else STARTS a new record. stanza's `recombine` takes ONE predicate and
has no third state, so the pattern fails in two directions:

- too **NARROW** → a stack trace fragments. Recoverable: a human reads two
  records instead of one, and every line keeps its own severity.
- too **BROAD** → **ordinary log lines are swallowed into the record above.**
  Unrelated events merge, the text is gone, and the blob is stamped with the
  FIRST line's severity — so an ERROR hides inside an INFO, invisible to every
  `severity>=ERROR` alert. This is the one that destroys data.

The three instances: `at\s` / `\d+\.\s` in 0.1.x's global pattern; the unbounded
dotted-identifier branch in spring/doris-fe/python (`2a40658`); and
`zap-console`'s `^[^\t\n]*\.[\w*()]+$` (`05365d7`), which turned 7 events into 3
records and destroyed two ERRORs — **worse than the catch-all**, which kept all 7.

### What it proves, and why there are no stack traces

`no-merge/<preset>.log` is **N ordinary, independent log lines** under that
format's most-stressed *plausible* config, and **no stack trace anywhere — not
one**. Assert `records == N`.

That is the whole trick. **A stack trace is the only thing that may legitimately
join a record.** With none present, any join at all is a preset swallowing an
ordinary log line, so the expected count needs no format knowledge to review: 4
lines means 4 records.

**This is what a pass-2 golden cannot do.** Every pass-2 corpus *contains* a
trace, so its record count conflates "joined the trace correctly" with "joined at
all". A too-broad continuation shows up there as a blessed number moving from 5
to 4 — which reads like a fix, and gets re-blessed. All three merges shipped
under green pass-2 goldens.

There is **no golden and no `--bless` path** here on purpose. N is `wc -l`, so
the only way to move the expectation is to add or remove real log lines, which is
visible in the diff as log lines.

The stress config per preset is the knob that produced a real bug — the corpus is
written as that config's output, since the config lives in the *emitting app*,
not in the receiver: spring `logging.pattern.console='%msg%n'`, doris-fe log4j2
`%msg%n`, python `format='%(name)s: %(message)s'`, go-stdlib `log.SetFlags(0)`,
postgres `log_line_prefix=''`, zap a message-only `EncoderConfig`, dotnet
`SingleLine=true`, mysql the MariaDB shape. `clickhouse` is the exception and is
the point of its own comment in `values.yaml`: its layout is **hardcoded**
(`OwnPatternFormatter`), so there is no knob to move and its corpus is the real
format, with the numbered prose in the MESSAGE where the layout can actually put
it.

A corpus may carry MORE THAN ONE plausible layout, because different layouts
stress different branches and one file cannot be two configs at once. `spring`
and `doris-fe` each carry both `%msg%n` (bare message at column 0, which is what
puts `at capacity, rejecting request` in front of the `^at\s+\S+\(.*\)$` branch)
and a logger-first `%logger{36}: %-5level %msg%n` (`com.foo.Bar: INFO started`,
which is what puts a dotted identifier in front of the dotted-identifier branch).
`python`'s single `format='%(name)s: %(message)s'` already produces the
dotted shape, so it needs no second layout. This is a deliberate departure from
"the corpus is one config's output": pass 3 asserts only that no line joins
another, and that is true of every plausible layout independently — so mixing
them costs nothing and covers a branch that would otherwise never be exercised.
The dotted lines are not decoration: the unbounded dotted branch is the merge
that shipped in `2a40658` and it was invisible to pass 3 until they were added,
because every line in those corpora began with a bare word.

### The two declared residuals

`go-stdlib` and `zap-console` **fail this guard today**, and both are declared in
`no-merge/residuals.yaml` rather than fixed or excluded:

- **`go-stdlib` 4 → 2.** Under `SetFlags(0)` the blank line hits `^$` and
  `handler.ServeHTTP(GET /healthz)` hits the unindented-frame branch. Neither can
  be bounded away without losing the panic dump the preset exists to join.
- **`zap-console` 6 → 4.** Under a message-only encoder, a message that is a
  single space-free dotted token is indistinguishable from a zap `main.main`
  frame at column 0.

Widening the presets to make these green would re-open the merge class; excluding
them would hide it. Instead the number is **declared and HELD**.

An entry needs an `expected` count and a `reason` — mandatory, rejected under 80
characters, and required to say which BRANCH swallows which LINE and why bounding
it was declined. The count is asserted **exactly**, in both directions:

- preset gets worse → count drops → **FAIL**.
- preset gets FIXED → count rises to N → **FAIL**, until the declaration is
  deleted. A residual cannot rot into a stale permission.
- `expected >= N` → **FAIL**. A residual only ever LOWERS the derived count.
- residual naming a preset that does not exist → **FAIL**.
- preset with no `no-merge/` corpus → **FAIL**. Driven off the CHART's preset
  list, like pass 2, so a new preset cannot ship unguarded.

That is the property the whole mechanism is for: **an accepted merge is a
reviewed number carrying its justification, not a comment nobody re-probes.**
Adding one is a diff in a file whose entire purpose is accepted data loss. The
alternative — a prose comment saying "this is safe" — is exactly how the
`clickhouse` "safe because it is scoped" contradiction survived three review
rounds.

Do not add a third entry to make a red build green. A third residual is a design
change and needs the argument that goes with one.

## Pass 3′ — the catch-all no-merge guard (`no-merge/catch-all.log`)

The catch-all now recombines too, on ONE rule and exactly one: `^\s` — a line
beginning with whitespace joins the record above. That is not a guess about any
format; it is a fact about loggers. Essentially none begin a NEW top-level event
with leading indentation, while nearly every multi-line continuation IS indented
— Java `\tat`, Python `  File "..."`, Go `\t/app/x.go`, .NET's six-space message
body — so `^\s` rejoins the bulk of every stack trace in ANY format with zero
format knowledge, and a column-0 top-level line always starts its own record.

`^\s` is safe **iff it never joins two independent top-level log lines.**
`TestCatchAllNoMerge` is that arbiter. It is the per-preset pass-3 idea applied to
the catch-all, which `TestNoMerge` cannot reach (the catch-all is not a preset and
never appears in `presets-values.yaml`). `no-merge/catch-all.log` is ordinary,
independent lines from formats the catch-all has NO preset for — a bare
`key=value` app log, a `2026/07/16 message`, an uppercase-level line, a JSON line,
a Rails-style request line and two exception headers — every one at COLUMN 0,
because that is the claim `^\s` rests on. The only indented lines are trace
CONTINUATIONS (a Java tab-frame pair and a Python space-indented traceback),
present to prove `^\s` DOES rejoin — the fragmentation win — without ever merging
an independent line.

The expected count is **derived, not blessed**: it is the number of app lines
whose payload does not match `^\s`, computed with the same predicate the chart
renders. A merge of two column-0 lines drops the record count below that and
FAILS; a continuation that stopped joining raises it above and FAILS too. There is
no golden and no bless path, exactly as for the per-preset guard: the only way to
move the expectation is to add or remove real log lines.

The risk `^\s` runs is a no-preset format whose ORDINARY output is legitimately
indented (an aligned banner, a pretty-printed config dump emitted line by line).
The corpus deliberately contains none, because none was found among real
no-preset formats — every logger surveyed writes its top-level lines at column 0.
If you ever find one, add its line to `no-merge/catch-all.log` as an independent
event: if `^\s` swallows it the test drops below N and fails, and that is the
signal that `^\s` is not safe for that format and it needs a preset instead.

## Preset corpora

Pass 2 reads `presets/<preset>.log` if it exists, else `corpora/<preset>.log`.
Every preset now has its own file under `presets/`, which pass 1 does NOT glob —
so a preset corpus can grow without disturbing the 12 catch-all goldens.

**Every one of these carries at least one VARIANT-CONFIG line**, and that is the
point of the file rather than a detail. A preset joins on what CONTINUES a
record (see `agent.logs.presets` in `values.yaml`), because a preset knows the
software but not the software's log-format configuration. The variant lines are
each format's mainstream config knob moved — Spring's
`logging.pattern.dateformat`, Postgres' `log_line_prefix`, Python's `datefmt=`,
Go's `log.SetFlags`, .NET's `TimestampFormat` — and under the old record-start
anchors they merged the whole stream into one record carrying the FIRST line's
severity. They must each produce their own record. If one of these files ever
drops back to a single record, the direction has been reverted:

- `presets/spring.log` — Spring Boot **2.x** (`yyyy-MM-dd HH:mm:ss.SSS`, and the
  `,SSS` comma-fraction variant) alongside 3.x's ISO instant, a stack trace with
  `Caused by:`/`... 3 more`, and the `trace-service ERROR` decoy.
  `corpora/java.log` is Boot 3.x only and cannot cover the 2.x regression.
  VARIANT: a `logging.pattern.dateformat=HH:mm:ss.SSS` pair (time-only, no date).
  Under the old `^\d{4}-` record anchor these merged the stream into ONE Info(9)
  record with the ERROR inside it. 13 lines -> 9 records.
- `presets/logfmt.log` — the quoted-decoy line
  (`msg="operator requested level=trace" level=error`) that no RE2 pattern can
  read correctly, plus a line with no `level` field at all. VARIANT: a Go panic
  in the middle of a logfmt stream. logfmt is the one preset with NO recombine at
  all (it is a one-line format by construction), and that panic is why: under the
  old `^\w+=` anchor every non-logfmt line was absorbed into the record above,
  so a panic arrived inside a `level=debug` record. 7 lines -> 7 records.
  **Synthetic** — see below.
- `presets/doris-fe.log` — Doris FE's `,SSS` timestamp + `(main|1)` thread field,
  and a Java stack trace (Doris FE is a Java service, so its continuation shapes
  are spring's). Doris **BE** is glog and belongs to the catch-all
  (`corpora/doris-be.log`). VARIANT: a time-only log4j pattern pair, which the
  old `^\d{4}-\d{2}-\d{2} ` anchor merged; they now fragment and degrade to
  `Unspecified(0)` (the severityRegex reads field 3, which the shorter timestamp
  moves). 7 lines -> 5 records. **Synthetic** — see below.
- `presets/postgres.log` — the `%q%u@%d` prefix including names with `-`/`.`
  (`my-app@my-db`), plus the Debian/RDS `user=%u,db=%d,app=%a` prefix that the
  preset deliberately does NOT read (it degrades to `Unspecified(0)`). The
  PREFIXED `STATEMENT:` line must get its OWN record — the `DETAIL|HINT|...`
  continuation branch is anchored at column 0 and must not reach it.
  VARIANT: a `log_line_prefix='[%p] '` pair (no timestamp at all), which the old
  anchor merged into one Info(9) record hiding the ERROR. 8 lines -> 8 records.
- `presets/python.log` — `logging.basicConfig()`'s default plus both common
  `%(asctime)s` overrides, an uncaught-exception traceback whose final
  `KeyError: 'id'` line is NOT indented (the line an indentation-based rule would
  break the record on), `CRITICAL` (the one Python level that is not an OTel
  name), and two decoys: `ERROR:trace-service:...` (a leftmost-wins window read
  the SERVICE NAME as Trace(1)) and an `INFO` line whose message contains
  `/error/` and `200`. The last two lines are an adjacent PAIR — an asctime line
  with NO fraction (what `datefmt=` emits) and one with a fraction (the asctime
  default) — pinning both sides of the optional `(?:[,.]\d{3})?`. They must be
  two records: requiring the fraction merged the no-fraction shape, and the
  whole stream with it. VARIANT: a `datefmt=` CRITICAL line, and a
  `format='%(levelname)s: %(message)s'` line (`DEBUG: msg`) — the latter is a
  NO-SWALLOW probe: it sits at column 0 like an exception line does, and the
  continuation's closed, CASE-SENSITIVE suffix set is what keeps it a record
  start (`Error` can never match `ERROR`). 22 lines -> 14 records.
- `presets/dotnet.log` — all six console-logger levels, including `fail` (Error)
  and `crit` (Fatal), the two-line-per-event shape, a `fail` event with a
  six-space-indented .NET stack trace, and a decoy category named
  `MyApp.Diagnostics.TraceService[0]` logging about an "error budget" at `info`.
  VARIANT: a `TimestampFormat`-prefixed event. .NET's is the arbitrary-format
  case — no record anchor could ever fit it — and under `^\s` it still
  RECOMBINES correctly (the six-space indent is untouched by the option), losing
  only its severity. 19 lines -> 8 records. **Synthetic** — see below.
- `presets/go-stdlib.log` — stdlib `log`'s `2026/07/16 09:00:00 msg` on stderr
  followed by a real panic: `panic:`, `[signal SIGSEGV...]`, a BLANK line,
  `goroutine 1 [running]:` and tab-indented frames. This corpus exists to prove
  that a preset with **no** `severityRegex` is still worth shipping, because it
  is the joining that was broken. The blank line is load-bearing: it must join,
  not terminate. `panic:` must START a record, not join the log line above it —
  that is worth 1 of the records below and it is the improvement the continuation
  direction unlocks. VARIANT: a `log.SetFlags(Ltime)` pair (no date), which the
  old slashed-date anchor merged. 12 lines -> 5 records, all `Unspecified(0)`.
- `presets/zap-console.log` — zap's console encoder in BOTH its real encoder
  configs, because they disagree on the two things a naive pattern would anchor
  to: `NewDevelopmentEncoderConfig` is CAPS + ISO8601, `NewProductionEncoderConfig`
  is lowercase + epoch float. Includes the multi-line stacktraces zap appends for
  Warn and above, and a `trace-service/main.go` + "error budget" decoy. The
  unindented `main.main` frame lines are why `^\s` alone is not enough here.
  VARIANT: a no-TimeEncoder pair, where the level shifts to field 1 — the old
  header anchor merged it. 14 lines -> 8 records.
- `presets/clickhouse.log` — `corpora/clickhouse.log`'s lines (the `<Level>`
  delimiter, a 36-char query UUID, and NUMBERED stack frames `0. DB::Exception`)
  plus a VARIANT: the pre-logger startup line ClickHouse writes to stderr before
  its config is read (`Processing configuration file '...'`), at column 0 with no
  timestamp. The old anchor appended it to the record above. 6 lines -> 4 records.
- `presets/mysql.log` — `corpora/mysql.log`'s MySQL 8 lines plus a VARIANT:
  MariaDB / MySQL 5.6's `2026-07-16  9:00:05 0 [Note] ...` shape, which uses a
  SPACE instead of the `T` separator the old `^\d{4}-\d{2}-\d{2}T` anchor
  required — so the whole error log merged. 6 lines -> 6 records.

## Captured vs. synthetic corpora

Most corpora here are **captured** — real output from the real component, so the
line shape is evidence of what that version actually writes.

**The VARIANT-CONFIG lines added to every preset corpus are SYNTHETIC**, without
exception, and that is a real limit on what they prove. Each was written by
applying the format's documented config knob to a captured line by hand — no
Spring app was rebooted with `logging.pattern.dateformat` set, no Postgres was
restarted with a changed `log_line_prefix`. They are evidence that the preset
handles *that shape*; they are not evidence that the shape is byte-for-byte what
the knob emits. The behaviour they pin — a variant line must not be swallowed
into the record above — does not depend on the exact spelling, which is why they
are worth having anyway. The `python` `datefmt=` shape is the one exception: it
was captured from a real interpreter (see below).

These corpora are synthetic in whole:

- `presets/doris-fe.log` — written from Doris FE's documented default log4j
  pattern, not captured from a running FE.
- `presets/logfmt.log` — logfmt is a convention rather than a product, so there
  is no canonical emitter to capture; these lines are written to the format's
  documented shape as `go-kit/log` and `logrus` emit it.
- `presets/clickhouse.log` — the ClickHouse lines are captured (they came from
  `corpora/clickhouse.log`), but the pre-logger startup line
  (`Processing configuration file '...'`) was written from ClickHouse's
  documented startup behaviour and **not captured from a running server**. What
  it pins is that a column-0 line with no timestamp starts its own record, which
  is true of any such line whatever its exact text.
- `presets/mysql.log` — the MySQL 8 lines are captured (from `corpora/mysql.log`);
  the MariaDB / MySQL 5.6 `2026-07-16  9:00:05 0 [Note]` lines are written from
  that format's documented shape and **not captured from a running MariaDB**.
- `presets/dotnet.log` — **no .NET SDK was available to capture from.** These
  lines are derived from `SimpleConsoleFormatter.cs` in `dotnet/runtime`, which
  is stronger evidence than documentation prose but is still not a running
  emitter: the six level strings are `GetLogLevelString`'s literal returns, the
  `": "` header separator is `LoglevelPadding`, and the six-space message indent
  is `_messagePadding` = `GetLogLevelString(Information).Length +
  LoglevelPadding.Length` = 4 + 2, computed rather than counted by eye. What it
  cannot prove is anything the source does not decide — most of all whether a
  real container emits ANSI color. The source gates color on
  `ConsoleUtils.EmitAnsiColorCodes`, which is `!Console.IsOutputRedirected` by
  default, and a container's stdout IS redirected, so it should be plain. That
  reasoning is unverified against a real pod.

The captured ones, and what they were captured from:

- `presets/python.log` — CPython 3.9 `logging`, all three `format=` shapes run
  for real. Only the clock digits were normalised to the corpora's `09:00:0X`.
- `presets/go-stdlib.log` — Go 1.26 `log` with default flags (`log.Flags() == 3`
  == `LstdFlags`), and a panic from a real nil-pointer dereference.
- `presets/zap-console.log` — zap v1.28 console encoder, both encoder configs,
  including the stacktraces zap itself appended.

This matters and is recorded rather than glossed: a synthetic corpus proves the
preset matches **what we believe the format looks like**, which is exactly the
assumption a preset can be wrong about. It cannot catch a real emitter that
differs from the documentation — a field we did not know was optional, padding
we guessed at, a version that moved the level. The honest reading of a green
`preset-doris-fe` / `preset-logfmt` / `preset-dotnet` is "consistent with the
documented format", not "verified against the real thing". Replace any of them
with captured output if you ever have a real node to take it from.

The zerolog console format was investigated and deliberately has **no** preset:
its `ConsoleWriter` colorizes by default even when stdout is redirected (`NoColor`
is a plain `bool`, so it is false unless set — verified by running it into a
pipe), so a real captured line wraps the level in ANSI escapes. zerolog's own
default is JSON, which the structural route already reads.

## What these goldens prove — pairs, not a sorted severity list

A golden is a canonical **multiset of {Body, Severity} PAIRS**. State the
guarantee precisely, and do not read it wider:

- **Caught:** a severity appearing, disappearing, or changing VALUE; a record
  count moving (a recombine that stopped joining, or started over-joining); a
  preset that TRANSPOSES two levels; a preset that mangles Body while naming the
  level correctly; a chart mutation that changes any of the above.
- **NOT caught:** the ORDER records are emitted in. See below — that is
  deliberate and measured, not an oversight.

This is the one thing the shell harness could not do. Its golden was a record
count plus a **sorted severity multiset** with Body never captured, so a preset
that transposed two levels shipped green: swap INFO and WARN between two lines
of `presets/spring.log` and `verify-formats.sh spring` printed `ok`. That was
demonstrated, documented as a known hole, and is now closed — the same swap
fails `TestPresets/spring` with the two moved pairs named, while the record count
(17) and the severity multiset are both unchanged.

### Why a multiset and not a list — do not retry ordering

Asserting severities **in file order** was tried and **measured as unstable**.
The reason is worth keeping so it is not retried blind:

The collector does not emit records in file order. The `source_identifier`
stdout/stderr split is one contributor (it makes `go.log` and `streams.log` two
independent recombine streams flushing on their own schedules), but it is **not
the whole cause and fixing only it is not enough**. `corpora/java.log` is 100%
stdout and gets no recombine at all under the catch-all, yet its single
`Warn(13)` still flips between position 6 and position 1 in roughly **1 run in
6** (measured 4/25; still 1/15 at `GOMAXPROCS=1`, so it is not merely a
parallelism artifact). Entries race between the stanza adapter's conversion of
entries into pdata and the batch the exporter finally sees; within a single
`ScopeLogs` the record order reflects that race rather than the file.

An order-asserting golden would therefore be a ~16%-flaky gate on at least one
corpus, and a flake trains everyone to re-bless without reading — which costs
more than the transposition it would catch.

Pairing each severity with the body that carries it is what buys the teeth
WITHOUT the flake: each record carries its own identity, so a transposition moves
a pair and the multiset notices, while the emission order stays free to race.
`TestCatchAll/java` was run 25× and `go`/`streams` 25× against this golden with
zero failures.

## Corpora

In pass 1 these mostly pin the REFUSAL described below; several are reused by
pass 2, where the same lines get a preset and must come out right.

- `java.log` — a Spring stack trace (all on stdout), plus a JSON line at line 5.
  In pass 1 the catch-all's `^\s` rule rejoins the trace: the
  `RuntimeException` header and its two indented `at` frames become ONE record
  (6→4), with no severity, except that JSON line, which still resolves to
  `Warn(13)` structurally. `preset-spring` (via `presets/spring.log`) is where the
  trace also picks up its severity.
- `go.log` — a panic that starts on stderr right after a benign stdout line.
  Mixes stdout/stderr, which is why goldens are sorted — see above. The one
  indented frame (`\t/app/server.go`) rejoins the `main.(*Server).handle` line
  under `^\s` (6→5); the `panic:`/`[signal`/`goroutine` headers stay at column 0
  and correctly stay separate.
- `python.log` — Python's `logging` default format (`LEVEL:name:message`) plus
  an uncaught-exception traceback. The catch-all's `^\s` rule joins the
  `Traceback` header with its four indented frame/context lines (11→7); the
  un-indented final `KeyError:` line stays its own record. `preset: python` reads
  the SAME file as 5, which is the same-file before/after the top-level README
  quotes.
- `dotnet.log` — the .NET default console logger's two-line shape
  (`info: Category[id]` + six-space-indented body). The catch-all's `^\s` rule
  joins each header to its indented body (and the fail event to its exception +
  stack frame), so it now records 3 — the SAME grouping as `preset: dotnet`,
  differing only in that the preset also names severity.
- `json.log` — pino-style structured logs with a **numeric** `level` field
  (10/20/30/40/50/60); the chart's json_parser severity mapping is itself
  numeric, so this resolves fully in the catch-all. A pure regression guard: if
  `json.txt` ever moves, structural JSON detection broke.
- `clickhouse.log` — a query-scoped line carrying a real 36-char UUID. Compare
  `clickhouse.txt` (no severity — catch-all) with `preset-clickhouse.txt`
  (`Debug(5)` — anchored to `<Level>`). The pair is the argument for scoping.
- `postgres.log` / `mysql.log` / `doris-be.log` / `k8s.log` — each engine's
  real default log line shape (Postgres `log_line_prefix`, MySQL 8 JSON-less
  error log, Doris BE / kubelet glog). The two glog corpora (`doris-be`, `k8s`)
  resolve fully in the catch-all; the other two need their preset.
- `streams.log` — a Python traceback arriving on stderr right after an unrelated
  INFO on stdout. Under `^\s` the catch-all rejoins the stderr traceback (the
  `Traceback` header plus its two indented frames) into one record while the two
  stdout INFO lines and the un-indented `KeyError:` stay separate (`records=4`).
  This is where the STREAM keying earns its keep: because recombine is keyed on
  path+stream, the stderr traceback cannot swallow the interleaved stdout INFO
  line that sits between its lines in the file. `source_identifier` is pinned by
  the `presets/`-driven pass-2 corpora and the recombine tests in
  `charts/fixter-collector/tests/agent_test.yaml`.
- `adversarial.log` — lines that must not be mislabelled. This is the corpus the
  redesign exists for; see below.

## What a pass-1 golden means now (this changed — read it)

Pass-1 goldens used to be *known-wrong baselines*: the catch-all guessed, and
several guesses were pinned here as bugs awaiting a fix. **That is no longer
true.** The catch-all no longer guesses at a level or a record START, so its
goldens are now correct-by-design, and the thing they pin is a deliberate refusal
plus one format-agnostic join.

The catch-all reads files whose format nobody has declared. It therefore:

- **recombines on `^\s` ONLY.** The multiline engine takes ONE predicate
  (`is_first_entry`) and appends everything else to the record above — there is
  no "neither" state — so a predicate that guesses at where a record STARTS
  merges *unrelated events* on any format it misreads. `^\s` is not that kind of
  guess: it describes a CONTINUATION (an indented line), and that is a fact about
  loggers rather than about a format — essentially none begin a new top-level
  event with leading whitespace, while nearly every stack-trace line IS indented.
  So a column-0 line always starts its own record and an indented line joins the
  one above, in ANY format. It is the SAFE negated direction (a miss SPLITS,
  never merges the stream) and it is held to `^\s` alone — every richer
  alternative swallowed real column-0 lines. `TestCatchAllNoMerge` is the guard
  that it never merges two independent lines; see below.
- **does not read a level out of the text.** Finding a level word means guessing
  where one sits, and a positional search guesses wrong in both directions.

So `Unspecified(0)` throughout a pass-1 text golden is the **right** answer, not
a gap: the catch-all does not know the format, so it says nothing. **A wrong
severity is worse than none — it is invisible to alerting either way, but it
also lies.** Do not "fix" a pass-1 golden by widening a pattern until a severity
appears; that is the bug this design removed.

Two things still resolve in the catch-all, because neither guesses at a position
— a body either IS a JSON object or it is not, and either DOES open with a glog
level letter or it does not:

- `json.txt` — every record keeps its severity (structural JSON).
- `k8s.txt`, `doris-be.txt` — every record keeps its severity (structural glog).
- `java.txt` keeps exactly one `Warn(13)`: corpora/java.log line 5 is a JSON line
  sitting among Spring text. That single surviving severity is the structural
  route proving it still works inside an otherwise-unreadable corpus.

`adversarial.txt` is now all `Unspecified(0)`, and that is the headline result of
the redesign. Five of the six severities it used to carry were **wrong**:

| line | was | now |
| --- | --- | --- |
| `trace-service ERROR ...` | Trace(1) — leftmost-wins read the service name | none |
| `[error-budget] INFO ...` | Error(17) — read the budget name | none |
| nginx `"GET /error/ ..." 200` | Error(17) — a 200 OK | none |
| `Sending trace to collector` | Trace(1) | none |
| `Reloading config from log: ...` | Info(9) — matched the `log:` mapping | none |
| `2026-07-16 09:00:00 INFO Starting up` | Info(9) — **correct** | none |

The last row is the whole trade, stated plainly: the same 48-char window that
read `INFO Starting up` correctly is what read the other five wrong, and no
tightening separates them — the catch-all cannot know which field is the level
without knowing the format. Giving up one right answer to delete five lies is
the trade this design makes on purpose. A `formats` entry recovers that line's
severity *correctly*, by anchoring to the format instead of hunting for a word.

### The text corpora keep NO severity, and their traces partially rejoin

`java`, `python`, `dotnet`, `go`, `streams`, `clickhouse`, `mysql` and
`postgres` all carry NO severity in the catch-all — that half is unchanged and is
the deliberate refusal above. What DID change on this branch is record count:
under the catch-all's `^\s` rule a stack trace's INDENTED lines rejoin the record
above, so `java` dropped 6→4, `python` 11→7, `go` 6→5, `dotnet` 8→3 and
`streams` 6→4. **This is the intended fragmentation win, not a regression.** The
join is partial by design: a trace line that is NOT indented (a Python `KeyError:`
final line, a Go `panic:`/`goroutine` header) stays its own record, because `^\s`
reaches only the indented lines and refuses to guess at anything else.
`clickhouse`, `mysql`, `postgres` and `adversarial` did NOT move at all — none of
their lines are indented — which is the clean statement that `^\s` touches only
continuations. A severity still needs a `formats` entry: compare `clickhouse.txt`
(all Unspecified) against `preset-clickhouse.txt` (Debug(5) on the query-scoped
line the old 48-char window could never reach).

`python`, `dotnet` and `go` also have presets (`python`, `dotnet`, `go-stdlib`,
`zap-console`), so the pass-1/pass-2 pair still shows the severity difference:
`python.txt` is 7 severity-less records and `preset-python.txt` is 5 correct ones
over the same corpus. `dotnet` now records the SAME grouping (3) in both passes —
the catch-all's `^\s` reaches .NET's six-space body just as the preset does — so
there the pair differs only in severity, which is the honest picture: joining is
format-agnostic, naming the level is not.

`go-stdlib` is the interesting one: its golden is `records=2` and BOTH are
`Unspecified(0)`. That is not a gap either — stdlib `log` emits no level, so the
preset joins the panic into one record and honestly reports no severity. A preset
that only fixes fragmentation is still worth shipping; fragmentation was always
the bigger half of the problem.

## Two hazards the in-process suite removed

Both of these were real and both are now structurally absent rather than worked
around. They are recorded so nobody reintroduces the shape that caused them.

**The self-telemetry port.** The collector's own telemetry defaults to a
Prometheus exporter on `localhost:8888`. The shell harness ran each corpus as a
separate PROCESS on the same host, so a process that had not yet released the
port made the next one fail at startup with "address already in use" and emit
zero LogRecords — indistinguishable from a `records=0` chart bug. It disabled
metrics telemetry in every generated run config and grepped stderr for the error
to make a collision loud. This suite builds the receiver in-process and starts no
service, no pipeline and no telemetry exporter, so there is no shared port to
collide on and cases can run in parallel.

**The 5s `force_flush_period` tail.** A recombine group flushes 5s after its last
line, OR immediately when the next record-start arrives. Every case used to pay
that 5s for its final group, which is most of what made a full run take ~8
minutes. Each corpus that goes through a RECOMBINING receiver now gets a
`SENTINEL flush marker…` line appended per stream — it is a record-start for
every preset, so the real final group flushes at once; the sentinel's own group
stays open and is never counted. This is self-checking: if the sentinel were ever
swallowed as a continuation, the real final group would not flush and the case
would come up one record SHORT and fail loudly. Measured: 12.6s → 0.9s across
three preset cases. The catch-all now recombines (on `^\s`) so it gets a sentinel
too — its column-0 prose is a record-start under `^\s`, so it flushes the real
final group and clears. Only the `logfmt` preset has no recombine and gets no
sentinel.
