// Harness for the format verification suite: render the CHART, build the
// filelog receiver the chart describes IN-PROCESS, feed it a corpus, collect
// what it emits.
//
// This replaces scripts/verify-formats.sh. The shell harness put two lossy
// parsers between the test and the truth: `yq` re-quotes strings on display
// (it has MASKED a real bug on this project), and python's yaml.safe_load
// silently keeps the LAST key on a duplicate — which is why a duplicate
// receiver name rendered clean here and only died at collector startup.
//
// Here the chart's config.yaml goes through the collector's OWN confmap, so
// the test parses config byte-for-byte the way the binary does. A duplicate
// key is an ERROR (gopkg.in/yaml.v3 refuses it) rather than a silent drop.
package formats

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/filelogreceiver"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"gopkg.in/yaml.v3"
)

// TestMain widens the parallel pool beyond GOMAXPROCS.
//
// Every case is dominated by WAITING for a receiver's records to settle, not by
// computing anything, so the useful concurrency is the number of cases rather
// than the number of CPUs. `go test` defaults -parallel to GOMAXPROCS, which on
// a 2-core runner serialised those waits and made the suite 5x slower (21.4s ->
// 4.4s here). An explicit -parallel on the command line still wins, because
// flag.Parse runs after this.
func TestMain(m *testing.M) {
	if f := flag.Lookup("test.parallel"); f != nil {
		_ = f.Value.Set("16")
	}
	os.Exit(m.Run())
}

// repoRoot is two levels up from test/formats.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// ---------------------------------------------------------------------------
// Chart rendering
// ---------------------------------------------------------------------------

// renderCache memoises `helm template` per argument set. Rendering is the only
// slow step left in the suite (~250ms), and pass 2 and pass 3 share a render.
var (
	renderMu    sync.Mutex
	renderCache = map[string]string{}
)

// renderAgentConfig runs `helm template` and returns the agent ConfigMap's
// config.yaml verbatim.
//
// It reads the CHART, never a copy. That is load-bearing: a reviewer proved the
// shell harness caught a chart mutation, and a test reading a checked-in copy of
// the config would not. Rendering here keeps that property.
func renderAgentConfig(t *testing.T, args ...string) string {
	t.Helper()
	key := strings.Join(args, "\x00")

	renderMu.Lock()
	defer renderMu.Unlock()
	if cached, ok := renderCache[key]; ok {
		return cached
	}

	root := repoRoot(t)
	full := append([]string{"template", "t", "charts/fixter-collector"}, args...)
	cmd := exec.Command("helm", full...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("helm template %v failed: %v\n%s", args, err, stderr)
	}

	cfg := extractAgentConfig(t, out)
	renderCache[key] = cfg
	return cfg
}

// extractAgentConfig pulls .data["config.yaml"] out of the agent ConfigMap in a
// multi-document helm render.
//
// Decoded with gopkg.in/yaml.v3 rather than piped through `yq`: yq's job in the
// shell harness was to PRINT a string back out, and its requoting on display is
// what masked a bug here once already. Nothing is re-serialised on this path —
// the string reaches confmap exactly as the chart emitted it.
func extractAgentConfig(t *testing.T, rendered []byte) string {
	t.Helper()

	type k8sDoc struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Data map[string]string `yaml:"data"`
	}

	dec := yaml.NewDecoder(strings.NewReader(string(rendered)))
	var found []string
	for {
		var doc k8sDoc
		err := dec.Decode(&doc)
		if err != nil {
			break
		}
		if doc.Kind != "ConfigMap" || !strings.Contains(doc.Metadata.Name, "agent") {
			continue
		}
		cfg, ok := doc.Data["config.yaml"]
		require.Truef(t, ok, "agent ConfigMap %q has no config.yaml", doc.Metadata.Name)
		found = append(found, cfg)
	}
	require.Lenf(t, found, 1, "expected exactly one agent ConfigMap, got %d", len(found))
	return found[0]
}

// chartPresets returns the preset names from the CHART's values.yaml.
//
// Driven off the chart, not off presets-values.yaml, so a preset added to the
// chart without a corpus FAILS here instead of shipping untested. That is the
// hole passes 2 and 3 exist to close; keep the source of truth as the chart.
func chartPresets(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "charts", "fixter-collector", "values.yaml"))
	require.NoError(t, err)

	var v struct {
		Agent struct {
			Logs struct {
				Presets map[string]yaml.Node `yaml:"presets"`
			} `yaml:"logs"`
		} `yaml:"agent"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &v))
	require.NotEmpty(t, v.Agent.Logs.Presets, "no presets in chart values.yaml")

	names := make([]string, 0, len(v.Agent.Logs.Presets))
	for n := range v.Agent.Logs.Presets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ---------------------------------------------------------------------------
// Receiver construction
// ---------------------------------------------------------------------------

// receiverConf returns the named receiver's config as a *confmap.Conf, taken
// from the rendered agent config through the collector's own confmap.
//
// This is the step the shell harness could not do. `confmap.NewRetrievedFromYAML`
// is literally what the binary calls on --config, so a config this test accepts
// is a config the binary accepts, and a duplicate receiver name is an error in
// BOTH — where python's safe_load kept the last one and rendered the bug clean.
func receiverConf(t *testing.T, agentYAML, name string) *confmap.Conf {
	t.Helper()

	retrieved, err := confmap.NewRetrievedFromYAML([]byte(agentYAML))
	require.NoError(t, err, "chart config.yaml is not parseable by the collector's confmap")
	conf, err := retrieved.AsConf()
	require.NoError(t, err)

	receivers, err := conf.Sub("receivers")
	require.NoError(t, err)

	all := receivers.ToStringMap()
	raw, ok := all[name]
	require.Truef(t, ok, "receiver %q not rendered; is it missing from presets-values.yaml? rendered: %v",
		name, receiverNames(all))

	m, ok := raw.(map[string]any)
	require.Truef(t, ok, "receiver %q is not a mapping", name)
	return confmap.NewFromStringMap(m)
}

func receiverNames(all map[string]any) []string {
	var out []string
	for k := range all {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// fileLogReceiverNames returns every file_log/* receiver in the rendered config.
func fileLogReceiverNames(t *testing.T, agentYAML string) []string {
	t.Helper()
	retrieved, err := confmap.NewRetrievedFromYAML([]byte(agentYAML))
	require.NoError(t, err)
	conf, err := retrieved.AsConf()
	require.NoError(t, err)
	receivers, err := conf.Sub("receivers")
	require.NoError(t, err)

	var out []string
	for k := range receivers.ToStringMap() {
		if strings.HasPrefix(k, "file_log") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// buildConfig turns a rendered receiver config into a typed *FileLogConfig
// pointed at dir.
//
// The include/start_at/exclude rewrite happens on the confmap BEFORE unmarshal,
// mirroring what the shell harness did with a python heredoc — but without the
// round-trip through safe_dump that silently normalised the config on the way.
func buildConfig(t *testing.T, sub *confmap.Conf, dir string) component.Config {
	t.Helper()

	m := sub.ToStringMap()
	m["include"] = []any{filepath.Join(dir, "*.log")}
	m["start_at"] = "beginning"
	// Pass 2's later formats carry an auto-generated exclude built from every
	// earlier format's globs. Both are repointed at THIS corpus, so the exclude
	// would blank the receiver under test.
	delete(m, "exclude")

	factory := filelogreceiver.NewFactory()
	cfg := factory.CreateDefaultConfig()
	require.NoError(t, confmap.NewFromStringMap(m).Unmarshal(cfg),
		"the chart's receiver config does not unmarshal into filelogreceiver's own config")
	return cfg
}

// hasRecombine reports whether this receiver's operator list contains a
// recombine.
//
// It decides whether a corpus gets a sentinel line (see writeCorpus). The
// catch-all now recombines too (on `^\s|^$`), so it gets a sentinel like any preset
// with a recombine. Only the `logfmt` preset has NO recombine — it is a one-line
// format by construction — so it emits every record immediately and a sentinel
// there would just be an extra record.
func hasRecombine(t *testing.T, sub *confmap.Conf) bool {
	t.Helper()
	arr, ok := sub.ToStringMap()["operators"].([]any)
	if !ok {
		return false
	}
	for _, o := range arr {
		if m, ok := o.(map[string]any); ok && m["type"] == "recombine" {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Corpus staging
// ---------------------------------------------------------------------------

// sentinelBody is appended to each stream of a corpus that goes through a
// recombining receiver.
//
// WHY: recombine flushes a group either 5s after its last line
// (force_flush_period) or IMMEDIATELY when the next record-start arrives. Without
// a sentinel every case pays a fixed 5s for its final group — which is most of
// what made the shell harness sleep. The sentinel is a record-start, so the real
// final group flushes at once; the sentinel's own group stays open and is never
// counted, because the test asserts the expected N records and tears the
// receiver down before force_flush_period elapses.
//
// It must not match ANY preset's continuation regex. It is prose at column 0
// with spaces in it, which clears all nine: the `^\s` branches (not indented),
// `^$` (not blank), the dotted-token branches in go-stdlib/zap-console (those
// require a space-free line), the Exception/Error suffix branches in
// spring/doris-fe/python (closed, case-sensitive suffix sets), postgres'
// DETAIL|HINT|... set, clickhouse's `^\d+\.\s`, and `^goroutine \d` /
// `^[signal ` / `^created by \S`.
//
// This is SELF-CHECKING and does not rest on that reading: if the sentinel were
// ever swallowed as a continuation, the real final group would not flush and the
// case would come up one record SHORT of its expectation and FAIL loudly. It
// cannot silently do the wrong thing.
const sentinelBody = "SENTINEL flush marker, not a record under test"

// writeCorpus stages a corpus in its own directory and returns the number of
// sentinel lines appended.
//
// Each corpus gets its OWN directory. source_identifier keys on
// `log.file.path|log.iostream`, so a shared directory would let one corpus'
// trailing recombine buffer leak into the next.
//
// A sentinel is appended PER STREAM present in the corpus, not once. stdout and
// stderr are two independent recombine groups (that is the whole point of
// source_identifier), so a stdout-only sentinel would leave a stderr group
// waiting out the full 5s.
func writeCorpus(t *testing.T, dir, corpusPath string, sentinel bool) {
	t.Helper()

	raw, err := os.ReadFile(corpusPath)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	content := string(raw)
	if sentinel {
		for _, stream := range streamsIn(t, corpusPath, content) {
			content += fmt.Sprintf("2026-07-16T23:59:59.999999999Z %s F %s\n", stream, sentinelBody)
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "0.log"), []byte(content), 0o644))
}

// criLine matches the CRI envelope every corpus line carries:
//
//	<RFC3339Nano> <stdout|stderr> <F|P> <the application's log line>
var criLine = regexp.MustCompile(`^\S+ (stdout|stderr) [FP] ?`)

// catchAllContinuation is the catch-all's continuation rule, `^\s|^$`, so
// TestCatchAllNoMerge derives its expectation from the corpus with the SAME
// predicate the chart renders — an app line beginning with whitespace OR empty
// joins the record above, everything else starts a new one. The `^$` branch
// exists because glog-style stack traces (Doris BE's "meet error status")
// separate the header from the indented frames with one EMPTY line, and `^\s`
// alone cannot match an empty string — the blank line started a new record and
// took the whole stack with it. See configmap-agent.yaml's file_log/default
// operators.
var catchAllContinuation = regexp.MustCompile(`^\s|^$`)

// streamsIn returns the distinct CRI streams present in a corpus, in a stable
// order. It also enforces the CRI envelope: a corpus line that is not CRI would
// be silently dropped by the container operator and quietly lower every count.
func streamsIn(t *testing.T, path, content string) []string {
	t.Helper()
	seen := map[string]bool{}
	var order []string
	for i, line := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		m := criLine.FindStringSubmatch(line)
		require.Truef(t, m != nil,
			"%s:%d is not CRI format (<RFC3339Nano> <stdout|stderr> <F|P> <line>): %q",
			path, i+1, line)
		if !seen[m[1]] {
			seen[m[1]] = true
			order = append(order, m[1])
		}
	}
	sort.Strings(order)
	return order
}

// ---------------------------------------------------------------------------
// Running a corpus
// ---------------------------------------------------------------------------

// record is one emitted log record, carrying its own identity.
//
// The shell harness captured a record COUNT plus a SORTED severity multiset and
// never captured Body at all — so a preset that transposed two levels between
// two lines shipped green. A {Body, Severity} pair fixes that: each record
// carries what it says AND what level it claims, so a transposition moves a pair
// and cannot hide.
//
// SevText is the resolved SeverityText — what is stamped downstream as `level` —
// which is not the numeric level's name: a glog `I` stays `I` unless
// overwrite_text canonicalises it to `INFO`.
type record struct {
	Body     string `json:"body"`
	Severity string `json:"severity"`
	SevText  string `json:"severity_text,omitempty"`
}

func (r record) String() string { return fmt.Sprintf("%s/%q\t%q", r.Severity, r.SevText, r.Body) }

// runCorpus feeds one corpus through the given receivers and returns every
// record they emitted.
//
// It waits for the sink to SETTLE rather than for an expected number, which
// replaces the shell harness' fixed `timeout 15`: a case finishes as soon as its
// records have stopped arriving. Waiting for an expected count would be circular
// (the golden decides when to stop reading, then is compared against what was
// read) and would return mid-batch if behaviour regressed to MORE records than
// the golden — the exact case a golden must catch. Settling has neither problem
// and, because every case runs in parallel, costs about one second for the whole
// suite.
//
// Multiple receivers share ONE sink, exactly as the shell harness put them in
// one pipeline behind one debug exporter. Pass 1 uses this with every file_log/*
// receiver in the default render; passes 2 and 3 name a single preset receiver,
// because pointing all of them at one corpus would run every preset over it at
// once and multiply every record.
func runCorpus(t *testing.T, subs []*confmap.Conf, corpusPath string) []record {
	t.Helper()
	return withoutSentinel(collect(runCorpusSink(t, subs, corpusPath)))
}

// runCorpusSink is runCorpus without the flatten, returning the settled sink so a
// caller can inspect attributes, which cannot live on the comparable `record`.
func runCorpusSink(t *testing.T, subs []*confmap.Conf, corpusPath string) *consumertest.LogsSink {
	t.Helper()
	require.NotEmpty(t, subs, "no receivers to run")

	sentinel := false
	for _, s := range subs {
		if hasRecombine(t, s) {
			sentinel = true
		}
	}

	dir := t.TempDir()
	writeCorpus(t, dir, corpusPath, sentinel)

	sink := new(consumertest.LogsSink)
	factory := filelogreceiver.NewFactory()

	for _, sub := range subs {
		cfg := buildConfig(t, sub, dir)
		set := receivertest.NewNopSettings(factory.Type())
		rcv, err := factory.CreateLogs(context.Background(), set, cfg, sink)
		require.NoError(t, err, "the chart's receiver config was rejected at construction")

		require.NoError(t, rcv.Start(context.Background(), componenttest.NewNopHost()),
			"the chart's receiver refused to start")
		t.Cleanup(func() { _ = rcv.Shutdown(context.Background()) })
	}

	waitSettled(t, sink)
	return sink
}

// attrByBody maps each emitted record's body to one attribute's value; present
// separates "absent" from "present but empty" (an empty ClickHouse `{}`).
func attrByBody(sink *consumertest.LogsSink, key string) (vals map[string]string, present map[string]bool) {
	vals, present = map[string]string{}, map[string]bool{}
	for _, ld := range sink.AllLogs() {
		for i := 0; i < ld.ResourceLogs().Len(); i++ {
			rl := ld.ResourceLogs().At(i)
			for j := 0; j < rl.ScopeLogs().Len(); j++ {
				sl := rl.ScopeLogs().At(j)
				for k := 0; k < sl.LogRecords().Len(); k++ {
					lr := sl.LogRecords().At(k)
					if v, ok := lr.Attributes().Get(key); ok {
						vals[lr.Body().AsString()] = v.AsString()
						present[lr.Body().AsString()] = true
					}
				}
			}
		}
	}
	return vals, present
}

// waitSettled blocks until the sink's record count has stopped moving.
//
// It never fails the test: a corpus that emits nothing settles at zero and is
// reported BY NAME by the caller, because "[streams] zero records" is a
// diagnosis and "condition never satisfied" is not.
func waitSettled(t *testing.T, sink *consumertest.LogsSink) {
	t.Helper()
	const (
		quiet = 1 * time.Second
		// A sink still at zero is given longer before it is believed: the
		// recombine force_flush_period is 5s, so anything short of that could be
		// mistaking a pending flush for an empty corpus.
		zeroFloor = 8 * time.Second
	)
	start := time.Now()
	deadline := start.Add(25 * time.Second)

	last, lastChange := -1, start
	for time.Now().Before(deadline) {
		n := len(withoutSentinel(collect(sink)))
		if n != last {
			last, lastChange = n, time.Now()
		} else if time.Since(lastChange) >= quiet && (n > 0 || time.Since(start) >= zeroFloor) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// collect flattens the sink into {Body, Severity} pairs.
func collect(sink *consumertest.LogsSink) []record {
	var out []record
	for _, ld := range sink.AllLogs() {
		for i := 0; i < ld.ResourceLogs().Len(); i++ {
			rl := ld.ResourceLogs().At(i)
			for j := 0; j < rl.ScopeLogs().Len(); j++ {
				sl := rl.ScopeLogs().At(j)
				for k := 0; k < sl.LogRecords().Len(); k++ {
					lr := sl.LogRecords().At(k)
					out = append(out, record{
						Body:     lr.Body().AsString(),
						Severity: severityString(lr.SeverityNumber()),
						SevText:  lr.SeverityText(),
					})
				}
			}
		}
	}
	return out
}

// severityString formats a severity the way the debug exporter does
// (`Error(17)`, `Unspecified(0)`), which is the spelling every golden in this
// directory has used since the shell harness wrote them.
func severityString(s plog.SeverityNumber) string {
	return fmt.Sprintf("%s(%d)", s.String(), int32(s))
}

// withoutSentinel drops the sentinel's own record if one was emitted.
//
// It normally is NOT: the sentinel opens a recombine group that stays open, so
// it never flushes within the test's life. This is the belt-and-braces path for
// a receiver that flushes it anyway (an operator change, a shutdown flush racing
// the read), so the sentinel can never be mistaken for a record under test.
func withoutSentinel(recs []record) []record {
	out := recs[:0:0]
	for _, r := range recs {
		if strings.Contains(r.Body, sentinelBody) {
			continue
		}
		out = append(out, r)
	}
	return out
}
