// The three passes. Read test/formats/README.md before changing what any of
// them ASSERTS — the goldens mean opposite things and each pass was paid for
// with a real bug.
//
//  1. the DEFAULT chart config (`formats: []`) -> file_log/default only, the
//     catch-all. Its goldens record a deliberate REFUSAL on severity: the
//     catch-all does not know the format, so JSON and glog resolve structurally
//     and every other text line gets NO severity. Unspecified(0) is the right
//     answer there, not a gap — never widen a pattern until a severity appears.
//     It DOES recombine on `^\s` (an indented line joins the record above), the
//     one format-agnostic continuation rule, so a stack trace's frames rejoin
//     while top-level lines stay separate — see TestCatchAllNoMerge, the guard
//     that `^\s` never merges two independent column-0 lines.
//  2. every preset -> one file_log/<preset> receiver each. Its goldens record
//     CORRECT values, always. Pass 2 exists because pass 1 renders no preset
//     receiver at all: a preset regex could rot to anything and pass 1 stayed
//     12/12 green. That is how the logfmt and Boot-2.x severity bugs shipped.
//  3. every preset again, over no-merge/ -> N ordinary, INDEPENDENT log lines
//     under that format's most-stressed plausible config, with NO stack trace
//     anywhere. Assert records == N. Pass 3 exists because every pass-2 corpus
//     CONTAINS a stack trace, so its record count conflates "joined the trace
//     correctly" with "joined at all" — a too-BROAD continuation is a blessed
//     number moving from 5 to 4, which reads like a fix. The merge bug shipped
//     three times under green pass-2 goldens.
//
// Run one corpus:  go test ./test/formats -run 'TestCatchAll/java'
//
//	go test ./test/formats -run 'TestPresets/spring'
//	go test ./test/formats -run 'TestNoMerge/zap-console'
//
// Re-bless passes 1 and 2 deliberately:  go test ./test/formats -bless
// Pass 3 has no golden and no bless path, on purpose.
package formats

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/confmap"
	"gopkg.in/yaml.v3"
)

var bless = flag.Bool("bless", false, "rewrite the pass-1 and pass-2 goldens from observed behaviour")

// ---------------------------------------------------------------------------
// Goldens
// ---------------------------------------------------------------------------

// A golden is a CANONICAL MULTISET of {Body, Severity} pairs: one JSON object
// per line, sorted by (severity, body).
//
// It is a multiset of PAIRS, not a sorted list of severities, and that is the
// whole upgrade over the shell harness. The old golden was a record count plus a
// SORTED severity multiset with Body never captured, so a preset that TRANSPOSED
// two levels — swap INFO and WARN between two lines — shipped green. That hole
// was known and documented. Pairing each severity with the body that carries it
// closes it: a transposition moves a pair, and the multiset notices.
//
// It is a MULTISET rather than a list because record ORDER IS NOT RELIABLE, and
// that was measured, not assumed. The collector does not emit in file order:
// corpora/java.log is 100% stdout, yet its single Warn(13) — the JSON line at
// line 5, which is its own record whether or not the trace around it recombines
// — still flips position in roughly 1 run in 6 (measured 4/25, and 1/15 even at
// GOMAXPROCS=1, so it is not a parallelism artifact). Entries
// race between the stanza adapter's conversion into pdata and the batch the
// exporter sees. An order-asserting golden would be a ~16%-flaky gate, and a
// flake trains everyone to re-bless without reading. Sorting the PAIRS keeps the
// per-record identity while tolerating that race — which is exactly the fix the
// old harness' own comment asked for and could not express.
func goldenPath(root, label string) string {
	return filepath.Join(root, "test", "formats", "expected", label+".jsonl")
}

func marshalGolden(recs []record) string {
	sorted := append(recs[:0:0], recs...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Severity != sorted[j].Severity {
			return sorted[i].Severity < sorted[j].Severity
		}
		if sorted[i].SevText != sorted[j].SevText {
			return sorted[i].SevText < sorted[j].SevText
		}
		return sorted[i].Body < sorted[j].Body
	})
	var b strings.Builder
	for _, r := range sorted {
		line, err := json.Marshal(r)
		if err != nil {
			panic(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func parseGolden(t *testing.T, path string) []record {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoErrorf(t, err, "no golden at %s; run: go test ./test/formats -bless", path)

	var out []record
	for i, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r record
		require.NoErrorf(t, json.Unmarshal([]byte(line), &r), "%s:%d is not valid JSON", path, i+1)
		out = append(out, r)
	}
	return out
}

// assertGolden compares observed records against the golden as a multiset of
// pairs, and reports the difference in both directions.
func assertGolden(t *testing.T, root, label string, got []record) {
	t.Helper()
	path := goldenPath(root, label)

	if *bless {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(marshalGolden(got)), 0o644))
		t.Logf("blessed [%s] records=%d", label, len(got))
		return
	}

	want := parseGolden(t, path)
	if marshalGolden(got) == marshalGolden(want) {
		t.Logf("ok [%s] records=%d", label, len(got))
		return
	}

	missing, extra := diffMultiset(want, got)
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] behaviour changed: records=%d, golden has %d\n", label, len(got), len(want))
	if len(missing) > 0 {
		b.WriteString("  in the golden, NOT emitted:\n")
		for _, r := range missing {
			fmt.Fprintf(&b, "    - %s\n", r)
		}
	}
	if len(extra) > 0 {
		b.WriteString("  emitted, NOT in the golden:\n")
		for _, r := range extra {
			fmt.Fprintf(&b, "    + %s\n", r)
		}
	}
	b.WriteString("  If this is a deliberate change, say why, then: go test ./test/formats -bless -run ")
	fmt.Fprintf(&b, "'.*/%s'", label)
	t.Error(b.String())
}

// diffMultiset returns pairs present in want but not got, and vice versa,
// honouring multiplicity.
func diffMultiset(want, got []record) (missing, extra []record) {
	count := map[record]int{}
	for _, r := range want {
		count[r]++
	}
	for _, r := range got {
		count[r]--
	}
	keys := make([]record, 0, len(count))
	for r := range count {
		keys = append(keys, r)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	for _, r := range keys {
		for n := count[r]; n > 0; n-- {
			missing = append(missing, r)
		}
		for n := count[r]; n < 0; n++ {
			extra = append(extra, r)
		}
	}
	return missing, extra
}

// ---------------------------------------------------------------------------
// Pass 1 — the catch-all, over every corpus
// ---------------------------------------------------------------------------

func TestCatchAll(t *testing.T) {
	root := repoRoot(t)
	agent := renderAgentConfig(t, "--set", "fixter.apiKey=dummy",
		"--set", "agent.logs.builtinFormats=null")

	names := fileLogReceiverNames(t, agent)
	require.NotEmpty(t, names, "the default render has no file_log receiver at all")

	corpora, err := filepath.Glob(filepath.Join(root, "test", "formats", "corpora", "*.log"))
	require.NoError(t, err)
	require.NotEmpty(t, corpora, "no corpora found")

	for _, corpus := range corpora {
		label := strings.TrimSuffix(filepath.Base(corpus), ".log")
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			subs := make([]*confmap.Conf, 0, len(names))
			for _, n := range names {
				subs = append(subs, receiverConf(t, agent, n))
			}
			got := runCorpus(t, subs, corpus)
			requireNonZero(t, label, got)
			assertGolden(t, root, label, got)
		})
	}
}

// ---------------------------------------------------------------------------
// Pass 2 — every preset, over the corpus of the same name
// ---------------------------------------------------------------------------

func TestPresets(t *testing.T) {
	root := repoRoot(t)
	agent := renderAgentConfig(t, "-f", "test/formats/presets-values.yaml")

	for _, preset := range chartPresets(t) {
		t.Run(preset, func(t *testing.T) {
			t.Parallel()

			// Pass 2 reads presets/<preset>.log if it exists, else
			// corpora/<preset>.log. presets/ is NOT globbed by pass 1, so a
			// preset corpus can grow without disturbing the 12 catch-all goldens.
			corpus := filepath.Join(root, "test", "formats", "presets", preset+".log")
			if _, err := os.Stat(corpus); err != nil {
				corpus = filepath.Join(root, "test", "formats", "corpora", preset+".log")
			}
			require.FileExistsf(t, corpus, "no corpus: add test/formats/presets/%s.log", preset)

			label := "preset-" + preset
			sub := receiverConf(t, agent, "file_log/"+preset)
			got := runCorpus(t, []*confmap.Conf{sub}, corpus)
			requireNonZero(t, label, got)
			assertGolden(t, root, label, got)
		})
	}
}

// requireNonZero reports a zero-record corpus BY NAME without aborting the rest
// of the run — a bare "0 != 17" says nothing about which corpus died.
func requireNonZero(t *testing.T, label string, got []record) {
	t.Helper()
	if len(got) == 0 {
		t.Errorf("[%s] zero records: the receiver started but emitted nothing. "+
			"Run this corpus alone before believing it: go test ./test/formats -run '.*/%s' -v", label, label)
	}
}

// ---------------------------------------------------------------------------
// ClickHouse query_id capture
// ---------------------------------------------------------------------------

// TestClickhouseQueryID locks the clickhouse preset's query_id capture into
// attributes.clickhouse_query_id, which the golden triple cannot see.
func TestClickhouseQueryID(t *testing.T) {
	root := repoRoot(t)
	agent := renderAgentConfig(t, "-f", "test/formats/presets-values.yaml")
	sub := receiverConf(t, agent, "file_log/clickhouse")

	sink := runCorpusSink(t, []*confmap.Conf{sub},
		filepath.Join(root, "test", "formats", "presets", "clickhouse.log"))
	vals, present := attrByBody(sink, "clickhouse_query_id")

	find := func(needle string) (body string) {
		t.Helper()
		for b := range present {
			if strings.Contains(b, needle) {
				return b
			}
		}
		for _, ld := range sink.AllLogs() {
			for i := 0; i < ld.ResourceLogs().Len(); i++ {
				rl := ld.ResourceLogs().At(i)
				for j := 0; j < rl.ScopeLogs().Len(); j++ {
					sl := rl.ScopeLogs().At(j)
					for k := 0; k < sl.LogRecords().Len(); k++ {
						if b := sl.LogRecords().At(k).Body().AsString(); strings.Contains(b, needle) {
							return b
						}
					}
				}
			}
		}
		t.Fatalf("no emitted record contains %q", needle)
		return ""
	}

	b := find("SELECT 1")
	require.True(t, present[b], "the {uuid} line carries no clickhouse_query_id attribute")
	require.Equal(t, "a1b2c3d4-e5f6-7890-abcd-ef1234567890", vals[b])

	b = find("UNKNOWN_TABLE")
	require.True(t, present[b], "the {abc} line carries no clickhouse_query_id attribute")
	require.Equal(t, "abc", vals[b])

	b = find("Ready for connections")
	require.True(t, present[b], "the empty {} line should carry a blank query_id, not nothing")
	require.Equal(t, "", vals[b])

	b = find("Processing configuration file")
	require.False(t, present[b], "a line with no {id} <Level> must not carry a clickhouse_query_id")
}

// ---------------------------------------------------------------------------
// Pass 3 — the no-merge guard
// ---------------------------------------------------------------------------

// residual is a DECLARED, accepted merge from test/formats/no-merge/residuals.yaml.
type residual struct {
	Expected *int   `yaml:"expected"`
	Reason   string `yaml:"reason"`
}

const reasonMinLen = 80

func loadResiduals(t *testing.T, root string) map[string]residual {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "test", "formats", "no-merge", "residuals.yaml"))
	require.NoError(t, err)
	var res map[string]residual
	require.NoError(t, yaml.Unmarshal(raw, &res))
	return res
}

// TestNoMerge asserts a record COUNT and nothing else, against a count DERIVED
// from the corpus rather than blessed.
//
// no-merge/<preset>.log is N ordinary, independent log lines under that format's
// most-stressed plausible config, and NO stack trace anywhere. A stack trace is
// the only thing that may legitimately join a record, so with none present the
// correct answer is always records == N: ANY join is a preset swallowing an
// ordinary log line into the record above, hiding its text and stamping it with
// the FIRST line's severity — an ERROR hidden inside an INFO, invisible to every
// severity>=ERROR alert.
//
// There is no golden and no bless path here on purpose. N is the line count, so
// the only way to move the expectation is to add or remove real log lines, which
// is visible in the diff as log lines.
func TestNoMerge(t *testing.T) {
	root := repoRoot(t)
	agent := renderAgentConfig(t, "-f", "test/formats/presets-values.yaml")
	residuals := loadResiduals(t, root)

	for _, preset := range chartPresets(t) {
		t.Run(preset, func(t *testing.T) {
			t.Parallel()

			corpus := filepath.Join(root, "test", "formats", "no-merge", preset+".log")
			require.FileExistsf(t, corpus, "no corpus: add test/formats/no-merge/%s.log — "+
				"N ordinary lines under this format's most-stressed plausible config, "+
				"NO stack traces. See test/formats/README.md.", preset)

			raw, err := os.ReadFile(corpus)
			require.NoError(t, err)
			lines := len(strings.Split(strings.TrimRight(string(raw), "\n"), "\n"))

			expected := lines
			declared := false
			if r, ok := residuals[preset]; ok {
				expected = validateResidual(t, preset, r, lines)
				declared = true
			}

			sub := receiverConf(t, agent, "file_log/"+preset)
			got := runCorpus(t, []*confmap.Conf{sub}, corpus)
			requireNonZero(t, "no-merge-"+preset, got)

			if len(got) == expected {
				if declared {
					t.Logf("ok [no-merge-%s] records=%d (declared residual; %d lines, %d swallowed)",
						preset, len(got), lines, lines-expected)
				} else {
					t.Logf("ok [no-merge-%s] records=%d of %d lines, no merge", preset, len(got), lines)
				}
				return
			}

			if len(got) < expected {
				t.Errorf("[no-merge-%s] MERGE: %d ordinary lines with no stack trace among them\n"+
					"  produced records=%d, expected %d. The continuationRegex swallowed %d log line(s)\n"+
					"  into the record above, hiding their text and their severity. Bound the branch\n"+
					"  that matched; do NOT declare a residual to make this green.\n%s",
					preset, lines, len(got), expected, expected-len(got), dumpRecords(got))
				return
			}

			// A DECLARED residual that no longer merges must FAIL until its
			// declaration is deleted, so an accepted merge cannot rot into a
			// stale permission nobody re-probed.
			t.Errorf("[no-merge-%s] records=%d, expected %d. This preset is declared in\n"+
				"  test/formats/no-merge/residuals.yaml as merging to %d, and it no longer does.\n"+
				"  If you bounded the branch, DELETE the declaration.",
				preset, len(got), expected, expected)
		})
	}
}

// validateResidual enforces the shape of a declared residual: a mandatory
// written reason naming which BRANCH swallows which LINE and why bounding it was
// declined, and a count that only ever LOWERS the derived one.
func validateResidual(t *testing.T, preset string, r residual, lines int) int {
	t.Helper()
	reason := strings.TrimSpace(r.Reason)
	require.GreaterOrEqualf(t, len(reason), reasonMinLen,
		"residuals.yaml [%s]: 'reason' is mandatory and must say which branch swallows which "+
			"line and why bounding it was declined (got %d chars, need %d)", preset, len(reason), reasonMinLen)
	require.NotNilf(t, r.Expected, "residuals.yaml [%s]: 'expected' must be an integer", preset)
	require.Lessf(t, *r.Expected, lines,
		"residuals.yaml [%s]: expected=%d does not LOWER the derived count (%d lines). A residual "+
			"only ever declares FEWER records than lines; delete the entry instead.", preset, *r.Expected, lines)
	return *r.Expected
}

// TestResidualsNameRealPresets fails a residual declared for a preset that no
// longer exists — a permission nobody is holding must not rot here quietly.
func TestResidualsNameRealPresets(t *testing.T) {
	root := repoRoot(t)
	presets := map[string]bool{}
	for _, p := range chartPresets(t) {
		presets[p] = true
	}
	for declared := range loadResiduals(t, root) {
		require.Truef(t, presets[declared],
			"residuals.yaml declares '%s', which is not a preset", declared)
	}
}

// TestCatchAllNoMerge is pass 3 for the CATCH-ALL. The per-preset TestNoMerge
// above cannot cover it — the catch-all is not a preset and never appears in
// presets-values.yaml — but it now carries a recombine of its own (`^\s`), so it
// gets the same guard: N ordinary, INDEPENDENT top-level log lines must produce N
// records, never fewer.
//
// The corpus is drawn from formats the catch-all has NO preset for — a bare
// key=value app log, a `2026/07/16 message`, an uppercase-level line, a JSON
// line, a couple of plain lines and two exception headers — every one of them at
// column 0, because that is the claim `^\s` rests on: essentially no logger
// begins a new top-level event with leading whitespace. The only indented lines
// are trace CONTINUATIONS (a Java tab frame set and a Python space-indented
// traceback), which legitimately join the record above and are the fragmentation
// win, not a merge.
//
// Expected N is DERIVED, not blessed: it is the count of app lines whose payload
// does not match `^\s`, computed with the very predicate the chart renders. A
// merge of two independent (column-0) lines drops the record count below N and
// FAILS; a continuation that stopped joining raises it above N and FAILS too.
// There is no golden and no bless path, exactly as for the per-preset guard.
func TestCatchAllNoMerge(t *testing.T) {
	root := repoRoot(t)
	agent := renderAgentConfig(t, "--set", "fixter.apiKey=dummy")

	corpus := filepath.Join(root, "test", "formats", "no-merge", "catch-all.log")
	raw, err := os.ReadFile(corpus)
	require.NoError(t, err)

	independent := 0
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		app := criLine.ReplaceAllString(line, "")
		if !catchAllContinuation.MatchString(app) {
			independent++
		}
	}
	require.NotZero(t, independent, "corpus has no top-level lines")

	sub := receiverConf(t, agent, "file_log/default")
	got := runCorpus(t, []*confmap.Conf{sub}, corpus)
	requireNonZero(t, "no-merge-catch-all", got)

	if len(got) < independent {
		t.Errorf("[no-merge-catch-all] MERGE: %d independent top-level lines produced records=%d.\n"+
			"  The catch-all's `^\\s` rule swallowed %d column-0 line(s) into the record above,\n"+
			"  hiding their text and their severity. `^\\s` is NOT safe as-is: a no-preset format\n"+
			"  whose ordinary output is legitimately indented is the risk. Do NOT ship it.\n%s",
			independent, len(got), independent-len(got), dumpRecords(got))
		return
	}
	if len(got) > independent {
		t.Errorf("[no-merge-catch-all] records=%d, expected %d: an indented trace continuation\n"+
			"  stopped joining the record above (the fragmentation the `^\\s` rule exists to fix).\n%s",
			len(got), independent, dumpRecords(got))
		return
	}
	t.Logf("ok [no-merge-catch-all] records=%d of %d lines, %d independent, no merge",
		len(got), len(strings.Split(strings.TrimRight(string(raw), "\n"), "\n")), independent)
}

func dumpRecords(recs []record) string {
	var b strings.Builder
	b.WriteString("  emitted:\n")
	for _, r := range recs {
		fmt.Fprintf(&b, "    %s\n", r)
	}
	return b.String()
}
