package report

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FilePrefix and FileTimeLayout name the report file. Every run writes its own
// timestamped file, so a directory of reports is a history of the suite rather
// than only its latest state.
const (
	FilePrefix     = "report-"
	FileTimeLayout = "20060102-150405"
)

// FileName is the report file name for a run that started at t.
func FileName(t time.Time) string {
	return FilePrefix + t.Format(FileTimeLayout) + ".html"
}

var funcs = template.FuncMap{
	"dur":   formatDuration,
	"pct":   func(f float64) string { return fmt.Sprintf("%.0f", f) },
	"stamp": func(t time.Time) string { return t.Format("2 Jan 2006, 15:04:05 MST") },
	"lower": strings.ToLower,
}

var page = template.Must(template.New("report").Funcs(funcs).Parse(pageHTML))

// WriteHTML renders the report into dir, creating it if needed, and returns the
// path written. The file name carries the run's timestamp.
func (r *Report) WriteHTML(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := page.Execute(&buf, r); err != nil {
		return "", err
	}
	path := filepath.Join(dir, FileName(r.StartedAt))
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// formatDuration renders a timing at a precision worth reading. A case with no
// reported timing renders as an em dash rather than a misleading "0s".
func formatDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "—"
	case d < time.Millisecond:
		return "<1ms"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.2fs", d.Seconds())
	default:
		return d.Round(time.Second).String()
	}
}

const pageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>katana report — {{.Project}} — {{stamp .StartedAt}}</title>
<style>
  :root {
    --bg: #f6f7f9; --panel: #ffffff; --ink: #16181d; --muted: #6b7280;
    --line: #e3e6ea; --pass: #17803d; --pass-bg: #e8f6ed; --fail: #c0261c;
    --fail-bg: #fdeceb; --skip: #8a6d1f; --skip-bg: #fbf3dd; --accent: #2f5fd0;
    --code: #f2f3f5;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #14161a; --panel: #1c1f24; --ink: #e8eaed; --muted: #9aa1ab;
      --line: #2c3037; --pass: #62c98b; --pass-bg: #16281d; --fail: #f2837a;
      --fail-bg: #2b1917; --skip: #dcc077; --skip-bg: #292215; --accent: #7ea2f0;
      --code: #15171b;
    }
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; padding: 2rem 1.25rem 4rem; background: var(--bg); color: var(--ink);
    font: 15px/1.55 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  }
  .wrap { max-width: 62rem; margin: 0 auto; }
  header { display: flex; flex-wrap: wrap; gap: 1rem; align-items: center; justify-content: space-between; margin-bottom: 1.5rem; }
  h1 { font-size: 1.4rem; margin: 0 0 .2rem; letter-spacing: -.01em; }
  h2 { font-size: 1rem; margin: 2rem 0 .75rem; text-transform: uppercase; letter-spacing: .08em; color: var(--muted); }
  .sub { margin: 0; color: var(--muted); font-size: .875rem; }
  .verdict { font-weight: 700; letter-spacing: .08em; text-transform: uppercase; padding: .5rem 1rem; border-radius: 999px; font-size: .8rem; }
  .verdict.pass { color: var(--pass); background: var(--pass-bg); }
  .verdict.fail { color: var(--fail); background: var(--fail-bg); }
  .tiles { display: grid; grid-template-columns: repeat(auto-fit, minmax(8rem, 1fr)); gap: .75rem; }
  .tile { background: var(--panel); border: 1px solid var(--line); border-radius: .6rem; padding: .85rem 1rem; }
  .tile .n { font-size: 1.6rem; font-weight: 650; letter-spacing: -.02em; }
  .tile .k { font-size: .75rem; text-transform: uppercase; letter-spacing: .07em; color: var(--muted); }
  .tile.pass .n { color: var(--pass); } .tile.fail .n { color: var(--fail); } .tile.skip .n { color: var(--skip); }
  .bar { height: .5rem; border-radius: 999px; background: var(--line); overflow: hidden; display: flex; margin: 1rem 0 0; }
  .bar i { display: block; height: 100%; }
  .bar .p { background: var(--pass); } .bar .f { background: var(--fail); } .bar .s { background: var(--skip); }
  .meta { background: var(--panel); border: 1px solid var(--line); border-radius: .6rem; padding: .5rem 1rem; margin-top: 1rem; }
  .meta dl { display: grid; grid-template-columns: max-content 1fr; gap: .35rem 1.25rem; margin: .5rem 0; }
  .meta dt { color: var(--muted); font-size: .8rem; text-transform: uppercase; letter-spacing: .06em; }
  .meta dd { margin: 0; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: .82rem; word-break: break-all; }
  .controls { display: flex; flex-wrap: wrap; gap: .5rem; align-items: center; margin-bottom: .75rem; }
  .controls button, .controls input {
    font: inherit; font-size: .85rem; padding: .35rem .8rem; border-radius: .4rem;
    border: 1px solid var(--line); background: var(--panel); color: var(--ink);
  }
  .controls button { cursor: pointer; }
  .controls button[aria-pressed="true"] { border-color: var(--accent); color: var(--accent); font-weight: 600; }
  .controls input { flex: 1; min-width: 10rem; }
  details.suite { background: var(--panel); border: 1px solid var(--line); border-radius: .6rem; margin-bottom: .6rem; }
  details.suite > summary {
    cursor: pointer; padding: .7rem 1rem; display: flex; gap: .6rem; align-items: center;
    flex-wrap: wrap; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: .85rem;
  }
  summary::-webkit-details-marker { display: none; }
  summary::before { content: "▸"; color: var(--muted); }
  details[open] > summary::before { content: "▾"; }
  .suite-name { flex: 1; word-break: break-all; }
  .tally { font-size: .75rem; font-family: inherit; color: var(--muted); display: flex; gap: .4rem; }
  .tally b { font-weight: 600; }
  .tally .p { color: var(--pass); } .tally .f { color: var(--fail); } .tally .s { color: var(--skip); }
  table { width: 100%; border-collapse: collapse; }
  tbody tr { border-top: 1px solid var(--line); }
  td { padding: .45rem 1rem; vertical-align: top; }
  td.st { width: 4.5rem; }
  td.du { width: 5.5rem; text-align: right; color: var(--muted); font-size: .8rem; white-space: nowrap; }
  .name { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: .85rem; word-break: break-word; }
  .pill { display: inline-block; font-size: .7rem; font-weight: 700; letter-spacing: .06em; text-transform: uppercase; padding: .1rem .45rem; border-radius: .3rem; }
  .pill.pass { color: var(--pass); background: var(--pass-bg); }
  .pill.fail { color: var(--fail); background: var(--fail-bg); }
  .pill.skip { color: var(--skip); background: var(--skip-bg); }
  pre { background: var(--code); border: 1px solid var(--line); border-radius: .4rem; padding: .7rem .85rem; margin: .5rem 0 .2rem; overflow-x: auto; font-size: .8rem; line-height: 1.45; }
  .note { background: var(--skip-bg); color: var(--skip); border-radius: .5rem; padding: .7rem 1rem; font-size: .85rem; margin-bottom: 1rem; }
  .behaviors td { font-size: .85rem; }
  .behaviors { background: var(--panel); border: 1px solid var(--line); border-radius: .6rem; overflow: hidden; }
  .behaviors td.mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: .8rem; }
  .stale { color: var(--skip); font-weight: 600; }
  footer { margin-top: 2.5rem; color: var(--muted); font-size: .8rem; }
  .empty { color: var(--muted); font-size: .85rem; padding: .5rem 0; }
</style>
</head>
<body>
<div class="wrap">

<header>
  <div>
    <h1>{{.Project}} — test report</h1>
    <p class="sub">{{stamp .StartedAt}} · {{dur .Duration}} · exit code {{.ExitCode}}</p>
  </div>
  <div class="verdict {{if .OK}}pass{{else}}fail{{end}}">{{.Result}}</div>
</header>

<section class="tiles">
  <div class="tile"><div class="n">{{.Total}}</div><div class="k">cases</div></div>
  <div class="tile pass"><div class="n">{{.Passed}}</div><div class="k">passed</div></div>
  <div class="tile fail"><div class="n">{{.Failed}}</div><div class="k">failed</div></div>
  <div class="tile skip"><div class="n">{{.Skipped}}</div><div class="k">skipped</div></div>
  <div class="tile"><div class="n">{{pct .PassRate}}%</div><div class="k">pass rate</div></div>
</section>

{{if .Total}}
<div class="bar">
  <i class="p" style="flex: {{.Passed}}"></i>
  <i class="f" style="flex: {{.Failed}}"></i>
  <i class="s" style="flex: {{.Skipped}}"></i>
</div>
{{end}}

<div class="meta">
  <dl>
    <dt>command</dt><dd>{{.Command}}</dd>
    <dt>project</dt><dd>{{.Root}}</dd>
    <dt>framework</dt><dd>{{if .Framework}}{{.Framework}}{{else}}(unset){{end}}</dd>
    <dt>katana</dt><dd>{{.Version}}</dd>
  </dl>
</div>

<h2>Test cases</h2>

{{if not .Parsed}}
<p class="note">katana could not recognise per-case results in this runner's output, so the
result below is the suite's exit code. The full output is at the end of this page.
Per-case results are recovered for go-test, pytest, jest, vitest, mocha, cargo-test, xunit and xctest.</p>
{{end}}

<div class="controls">
  <button data-filter="all" aria-pressed="true">All</button>
  <button data-filter="fail" aria-pressed="false">Failed</button>
  <button data-filter="pass" aria-pressed="false">Passed</button>
  <button data-filter="skip" aria-pressed="false">Skipped</button>
  <input id="q" type="search" placeholder="Filter by name…" autocomplete="off">
</div>

{{range .Suites}}
<details class="suite"{{if .Failed}} open{{end}}>
  <summary>
    <span class="suite-name">{{.Name}}</span>
    <span class="tally">
      {{if .Passed}}<span class="p"><b>{{.Passed}}</b> passed</span>{{end}}
      {{if .Failed}}<span class="f"><b>{{.Failed}}</b> failed</span>{{end}}
      {{if .Skipped}}<span class="s"><b>{{.Skipped}}</b> skipped</span>{{end}}
      <span>{{dur .Duration}}</span>
    </span>
  </summary>
  <table>
    <tbody>
    {{range .Cases}}
      <tr data-status="{{.Status}}" data-name="{{lower .Name}}">
        <td class="st"><span class="pill {{.Status}}">{{.Status}}</span></td>
        <td class="name">{{.Name}}{{if .Output}}<pre>{{.Output}}</pre>{{end}}</td>
        <td class="du">{{dur .Duration}}</td>
      </tr>
    {{end}}
    </tbody>
  </table>
</details>
{{end}}
<p class="empty" id="no-match" hidden>No test case matches this filter.</p>

{{if .Behaviors}}
<h2>Behaviors</h2>
{{if .StaleBehaviors}}
<p class="note">{{.StaleBehaviors}} behavior(s) were out of date with their generated tests when this
suite ran, so these results do not fully cover the current specification. Run <code>katana generate</code>.</p>
{{end}}
<table class="behaviors">
  <tbody>
  {{range .Behaviors}}
    <tr>
      <td class="mono">{{.Source}}</td>
      <td class="mono">{{.Output}}</td>
      <td{{if .Stale}} class="stale"{{end}}>{{.Status}}</td>
      <td>{{.Stack}}</td>
    </tr>
  {{end}}
  </tbody>
</table>
{{end}}

{{if .Output}}
<h2>Suite output</h2>
<details>
  <summary>Full output of <code>{{.Command}}</code></summary>
  <pre>{{.Output}}</pre>
</details>
{{end}}

<footer>Generated by katana {{.Version}} · <code>katana run --save</code></footer>
</div>

<script>
(function () {
  var status = 'all';
  var q = document.getElementById('q');
  var buttons = document.querySelectorAll('.controls button');
  var suites = document.querySelectorAll('details.suite');
  var none = document.getElementById('no-match');

  function apply() {
    var term = q.value.trim().toLowerCase();
    var shownTotal = 0;
    suites.forEach(function (suite) {
      var shown = 0;
      suite.querySelectorAll('tbody tr').forEach(function (row) {
        var ok = (status === 'all' || row.dataset.status === status) &&
                 (term === '' || row.dataset.name.indexOf(term) !== -1);
        row.hidden = !ok;
        if (ok) shown++;
      });
      suite.hidden = shown === 0;
      shownTotal += shown;
      if (shown > 0 && (status !== 'all' || term !== '')) suite.open = true;
    });
    none.hidden = shownTotal > 0;
  }

  buttons.forEach(function (b) {
    b.addEventListener('click', function () {
      status = b.dataset.filter;
      buttons.forEach(function (o) { o.setAttribute('aria-pressed', String(o === b)); });
      apply();
    });
  });
  q.addEventListener('input', apply);
})();
</script>
</body>
</html>
`
