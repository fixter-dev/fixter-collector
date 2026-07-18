package formats

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/filelogreceiver"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"
)

const duplicateCorpusLines = 10

func writeDuplicateCorpus(t *testing.T, path string, lines int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	var b []byte
	for i := 1; i <= lines; i++ {
		b = append(b, []byte(fmt.Sprintf(
			"2026-07-16T09:00:%02d.000000000Z stdout F 2026-07-16T09:00:%02d.000Z INFO line %d\n",
			i, i, i))...)
	}
	require.NoError(t, os.WriteFile(path, b, 0o644))
}

func rawReceiver(t *testing.T, sub *confmap.Conf, overrideInclude []string) component.Config {
	t.Helper()
	m := sub.ToStringMap()
	if overrideInclude != nil {
		anys := make([]any, len(overrideInclude))
		for i, s := range overrideInclude {
			anys[i] = s
		}
		m["include"] = anys
	}
	m["start_at"] = "beginning"

	factory := filelogreceiver.NewFactory()
	cfg := factory.CreateDefaultConfig()
	require.NoError(t, confmap.NewFromStringMap(m).Unmarshal(cfg),
		"the chart's receiver config does not unmarshal into filelogreceiver's own config")
	return cfg
}

func runRawReceivers(t *testing.T, cfgs []component.Config) []record {
	t.Helper()
	sink := new(consumertest.LogsSink)
	factory := filelogreceiver.NewFactory()
	for _, cfg := range cfgs {
		set := receivertest.NewNopSettings(factory.Type())
		rcv, err := factory.CreateLogs(context.Background(), set, cfg, sink)
		require.NoError(t, err, "the chart's receiver config was rejected at construction")
		require.NoError(t, rcv.Start(context.Background(), componenttest.NewNopHost()),
			"the chart's receiver refused to start")
		t.Cleanup(func() { _ = rcv.Shutdown(context.Background()) })
	}
	waitSettled(t, sink)
	return withoutSentinel(collect(sink))
}

// TestNoDuplicateCollection proves the chart's auto-generated `exclude` stops one
// file being read TWICE. Two formats whose include globs OVERLAP (broad matching
// prod_*, narrow matching prod_api_*) both claim the same file; earlier formats
// take precedence, so the chart excludes each earlier format's globs from every
// later one, and only the earlier (narrow) receiver reads the file.
//
// A render assertion cannot see this — only real receivers over a real file can:
// without the exclude both receivers read the corpus and every record is emitted
// TWICE, silently doubling the customer's ingest bill with no error anywhere.
//
// The include AND exclude are kept exactly as the CHART rendered them; nothing
// here writes an exclude. The catch-all is repointed at nothing so only the two
// format receivers can produce records. See configmap-agent.yaml's `lt $j $i`
// precedence guard. This replaces scripts/verify-no-duplicate-collection.sh.
func TestNoDuplicateCollection(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	corpus := filepath.Join(dir, "fmt", "prod_api_1", "app", "0.log")
	writeDuplicateCorpus(t, corpus, duplicateCorpusLines)

	valuesPath := filepath.Join(dir, "values.yaml")
	values := fmt.Sprintf(`fixter:
  apiKey: dummy
agent:
  logs:
    formats:
      - name: narrow
        include: ["%s/fmt/prod_api_*/*/*.log"]
      - name: broad
        include: ["%s/fmt/prod_*/*/*.log"]
`, dir, dir)
	require.NoError(t, os.WriteFile(valuesPath, []byte(values), 0o644))

	agent := renderAgentConfig(t, "-f", valuesPath,
		"--set", "agent.logs.builtinFormats=null")

	require.ElementsMatch(t,
		[]string{"file_log/narrow", "file_log/broad", "file_log/default"},
		fileLogReceiverNames(t, agent),
		"expected the two overlapping formats plus the catch-all")

	cfgs := []component.Config{
		rawReceiver(t, receiverConf(t, agent, "file_log/narrow"), nil),
		rawReceiver(t, receiverConf(t, agent, "file_log/broad"), nil),
		rawReceiver(t, receiverConf(t, agent, "file_log/default"),
			[]string{filepath.Join(dir, "none", "*", "*.log")}),
	}

	got := runRawReceivers(t, cfgs)

	if len(got) == duplicateCorpusLines*2 {
		t.Fatalf("[no-duplicate] every record DUPLICATED: both file_log/narrow and file_log/broad "+
			"read the same file. The chart's precedence exclude regressed — the later, broader format "+
			"must exclude the earlier, narrower format's glob.\n  corpus lines: %d, records emitted: %d",
			duplicateCorpusLines, len(got))
	}
	require.Equalf(t, duplicateCorpusLines, len(got),
		"[no-duplicate] corpus lines: %d, records emitted: %d — each line must be collected exactly once",
		duplicateCorpusLines, len(got))
	t.Logf("ok [no-duplicate] each of %d lines collected exactly once", duplicateCorpusLines)
}
