package dashboard

import "html/template"

var dashboardTemplate = template.Must(template.New("dashboard").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>wait0 dashboard</title>
  <style>
    :root {
      --bg: #f4f6f7;
      --panel: #ffffff;
      --text: #18222b;
      --muted: #5f6d7a;
      --border: #d6dde3;
      --accent: #136f63;
      --accent-soft: #e8f6f2;
      --danger: #b00020;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      font-family: "Iosevka Aile", "IBM Plex Sans", "Segoe UI", sans-serif;
      color: var(--text);
      background: radial-gradient(1200px 500px at 10% -10%, #e7f6ef 0%, transparent 70%), var(--bg);
    }
    .wrap {
      max-width: 1100px;
      margin: 0 auto;
      padding: 24px 16px 32px;
    }
    .header {
      margin-bottom: 16px;
    }
    h1 {
      margin: 0;
      font-family: "IBM Plex Mono", "Iosevka", monospace;
      font-size: 28px;
      letter-spacing: 0.02em;
    }
    .muted {
      color: var(--muted);
      font-size: 14px;
    }
    .grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
      gap: 12px;
      margin-bottom: 18px;
    }
    .card {
      background: var(--panel);
      border: 1px solid var(--border);
      border-radius: 10px;
      padding: 12px;
      box-shadow: 0 1px 0 rgba(24, 34, 43, 0.04);
    }
    .card h2 {
      margin: 0 0 6px;
      font-size: 13px;
      text-transform: uppercase;
      letter-spacing: 0.04em;
      color: var(--muted);
    }
    .metric {
      font-family: "IBM Plex Mono", monospace;
      font-size: 24px;
      margin: 0;
    }
    .charts {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
      gap: 12px;
      margin-bottom: 18px;
    }
    .chart {
      min-height: 130px;
    }
    .chart svg {
      width: 100%;
      height: 92px;
      display: block;
      border-top: 1px solid var(--border);
      margin-top: 8px;
      padding-top: 8px;
    }
    form {
      display: grid;
      gap: 10px;
    }
    label {
      font-size: 13px;
      color: var(--muted);
      display: block;
      margin-bottom: 4px;
    }
    textarea {
      width: 100%;
      min-height: 80px;
      padding: 10px;
      border: 1px solid var(--border);
      border-radius: 8px;
      font-family: "IBM Plex Mono", monospace;
      font-size: 13px;
      resize: vertical;
    }
    button {
      border: none;
      border-radius: 8px;
      padding: 10px 14px;
      font-weight: 600;
      color: #fff;
      background: var(--accent);
      cursor: pointer;
      width: fit-content;
    }
    button:disabled {
      background: #97aaa6;
      cursor: not-allowed;
    }
    .status {
      font-size: 13px;
      white-space: pre-wrap;
      margin-top: 4px;
    }
    .ok { color: var(--accent); }
    .err { color: var(--danger); }
    .note {
      margin-top: 4px;
      color: var(--muted);
      font-size: 13px;
    }
    .section-title {
      margin: 22px 0 10px;
      font-size: 18px;
    }
    .rule-list {
      display: grid;
      gap: 14px;
      margin-bottom: 18px;
    }
    .rule-card {
      padding: 14px;
    }
    .rule-heading {
      display: flex;
      justify-content: space-between;
      align-items: baseline;
      gap: 12px;
      margin-bottom: 10px;
    }
    .rule-heading code {
      font-family: "IBM Plex Mono", "Iosevka", monospace;
      font-size: 15px;
      overflow-wrap: anywhere;
    }
    .rule-metrics {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
      gap: 8px;
      margin-bottom: 12px;
    }
    .rule-metric {
      background: var(--accent-soft);
      border-radius: 8px;
      padding: 9px;
    }
    .rule-metric span {
      display: block;
      color: var(--muted);
      font-size: 11px;
      text-transform: uppercase;
      letter-spacing: 0.04em;
      margin-bottom: 3px;
    }
    .rule-metric strong {
      font-family: "IBM Plex Mono", monospace;
      font-size: 14px;
      overflow-wrap: anywhere;
    }
    .rankings {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(230px, 1fr));
      gap: 10px;
    }
    .ranking {
      border: 1px solid var(--border);
      border-radius: 8px;
      padding: 9px;
      min-width: 0;
    }
    .ranking h3 {
      margin: 0 0 7px;
      color: var(--muted);
      font-size: 12px;
      text-transform: uppercase;
      letter-spacing: 0.04em;
    }
    .ranking ol {
      margin: 0;
      padding-left: 22px;
    }
    .ranking li {
      margin: 4px 0;
      font-size: 12px;
    }
    .ranking-row {
      display: flex;
      justify-content: space-between;
      align-items: baseline;
      gap: 8px;
    }
    .ranking-row code {
      overflow-wrap: anywhere;
      min-width: 0;
    }
    .ranking-value {
      color: var(--muted);
      white-space: nowrap;
      font-family: "IBM Plex Mono", monospace;
    }
  </style>
</head>
<body>
  <div class="wrap">
    <div class="header">
      <h1>wait0 dashboard</h1>
      <p class="muted">Polling every <span id="poll-ms"></span>ms from <code>/wait0/dashboard/stats</code></p>
    </div>

    <div class="grid">
      <div class="card"><h2>Cached URLs</h2><p class="metric" id="m-urls">-</p></div>
      <div class="card"><h2>Total Cached Size</h2><p class="metric" id="m-size">-</p></div>
      <div class="card"><h2>RSS Memory</h2><p class="metric" id="m-rss">-</p></div>
      <div class="card"><h2>Go Alloc</h2><p class="metric" id="m-alloc">-</p></div>
      <div class="card"><h2>Refresh Avg</h2><p class="metric" id="m-refresh-avg">-</p></div>
      <div class="card"><h2>Sitemap Crawl</h2><p class="metric" id="m-crawl">-</p></div>
    </div>

    <div class="charts">
      <div class="card chart">
        <h2>URLs Over Time</h2>
        <div id="c-urls" class="muted"></div>
      </div>
      <div class="card chart">
        <h2>RSS Over Time</h2>
        <div id="c-rss" class="muted"></div>
      </div>
      <div class="card chart">
        <h2>Refresh Avg (ms)</h2>
        <div id="c-refresh" class="muted"></div>
      </div>
    </div>

    <h2 class="section-title">Rules</h2>
    <div id="rules-list" class="rule-list">
      <div class="card muted">Loading rules...</div>
    </div>

    <div class="card">
      <h2>Invalidate Cache</h2>
      <form id="invalidate-form">
        <div>
          <label for="paths">Paths (comma or newline separated)</label>
          <textarea id="paths" placeholder="/products/123\n/"></textarea>
        </div>
        <div>
          <label for="tags">Tags (comma or newline separated)</label>
          <textarea id="tags" placeholder="product:123\nhomepage"></textarea>
        </div>
        <button id="invalidate-btn" type="submit">Invalidate</button>
        <div id="invalidate-note" class="note"></div>
        <div id="invalidate-status" class="status muted"></div>
      </form>
    </div>
  </div>

  <script>
    const cfg = {
      pollMs: {{.PollIntervalMS}},
      invalidationEnabled: {{if .InvalidationEnabled}}true{{else}}false{{end}},
      csrfToken: '{{.CSRFToken}}',
      maxPoints: 120,
      invalidateStatusClearMs: 8000,
    };

    const history = [];
    let invalidateStatusTimer = null;

    const byId = (id) => document.getElementById(id);

    function splitList(input) {
      return input
        .split(/[\n,]/g)
        .map((x) => x.trim())
        .filter((x) => x.length > 0);
    }

    function toHumanBytes(v) {
      const n = Number(v || 0);
      if (!Number.isFinite(n) || n <= 0) return '0 B';
      const units = ['B', 'KB', 'MB', 'GB', 'TB'];
      let i = 0;
      let x = n;
      while (x >= 1024 && i < units.length - 1) {
        x /= 1024;
        i++;
      }
      return x.toFixed(x >= 10 || i === 0 ? 0 : 1) + ' ' + units[i];
    }

    function toNum(v) {
      const n = Number(v);
      return Number.isFinite(n) ? n : 0;
    }

    function toHumanDuration(v) {
      const ms = Number(v);
      if (!Number.isFinite(ms) || ms < 0) return '-';
      if (ms < 1000) return ms.toFixed(ms < 10 ? 1 : 0) + ' ms';
      const seconds = ms / 1000;
      if (seconds < 60) return seconds.toFixed(seconds < 10 ? 1 : 0) + ' s';
      const minutes = seconds / 60;
      return minutes.toFixed(minutes < 10 ? 1 : 0) + ' min';
    }

    function secondsAgo(iso) {
      if (!iso) return 'not run yet';
      const finished = Date.parse(iso);
      if (!Number.isFinite(finished)) return 'not run yet';
      const seconds = Math.max(0, Math.floor((Date.now() - finished) / 1000));
      return seconds + 's ago';
    }

    function makeElement(tag, className, text) {
      const element = document.createElement(tag);
      if (className) element.className = className;
      if (text !== undefined) element.textContent = text;
      return element;
    }

    function appendRuleMetric(host, label, value) {
      const metric = makeElement('div', 'rule-metric');
      metric.appendChild(makeElement('span', '', label));
      metric.appendChild(makeElement('strong', '', value));
      host.appendChild(metric);
    }

    function makeRanking(title, items, valueKey, formatter, emptyText) {
      const ranking = makeElement('div', 'ranking');
      ranking.appendChild(makeElement('h3', '', title));
      if (!Array.isArray(items) || items.length === 0) {
        ranking.appendChild(makeElement('div', 'muted', emptyText));
        return ranking;
      }
      const list = makeElement('ol');
      for (const item of items) {
        const row = makeElement('div', 'ranking-row');
        row.appendChild(makeElement('code', '', String(item?.url || '-')));
        row.appendChild(makeElement('span', 'ranking-value', formatter(item?.[valueKey])));
        const li = makeElement('li');
        li.appendChild(row);
        list.appendChild(li);
      }
      ranking.appendChild(list);
      return ranking;
    }

    function renderRules(payload) {
      const host = byId('rules-list');
      if (!host) return;
      host.replaceChildren();
      const rules = Array.isArray(payload?.rules) ? payload.rules : [];
      if (rules.length === 0) {
        host.appendChild(makeElement('div', 'card muted', 'No rules configured.'));
        return;
      }

      for (const rule of rules) {
        const card = makeElement('section', 'card rule-card');
        const heading = makeElement('div', 'rule-heading');
        heading.appendChild(makeElement('code', '', String(rule?.match || '-')));
        heading.appendChild(makeElement('span', 'muted', 'priority ' + toNum(rule?.priority)));
        card.appendChild(heading);

        const warmup = rule?.warmup || {};
        const warmupConfigured = warmup?.configured === true;
        const duration = !warmupConfigured
          ? '- not set'
          : warmup?.last_loop_duration_ms == null
            ? 'not run yet'
            : toHumanDuration(warmup.last_loop_duration_ms);
        const finished = !warmupConfigured ? '- not set' : secondsAgo(warmup?.last_finished_at);

        const metrics = makeElement('div', 'rule-metrics');
        appendRuleMetric(metrics, 'URLs', String(toNum(rule?.urls)));
        appendRuleMetric(metrics, 'Responses', String(toNum(rule?.responses)));
        appendRuleMetric(metrics, 'RAM size', toHumanBytes(rule?.ram_size_bytes));
        appendRuleMetric(metrics, 'Disk size', toHumanBytes(rule?.disk_size_bytes));
        appendRuleMetric(metrics, 'Last warmup loop', duration);
        appendRuleMetric(metrics, 'Warmup finished', finished);
        appendRuleMetric(
          metrics,
          'Pause between runs',
          warmupConfigured ? toHumanDuration(warmup?.pause_between_runs_ms) : '- not set'
        );
        card.appendChild(metrics);

        const noWarmupData = !warmupConfigured ? 'Warmup not set.' : 'No completed warmup data.';
        const rankings = makeElement('div', 'rankings');
        rankings.appendChild(makeRanking(
          '10 slowest · last loop',
          warmup?.top_slowest_urls,
          'duration_ms',
          toHumanDuration,
          noWarmupData
        ));
        rankings.appendChild(makeRanking(
          '10 fastest · last loop',
          warmup?.top_fastest_urls,
          'duration_ms',
          toHumanDuration,
          noWarmupData
        ));
        rankings.appendChild(makeRanking(
          '10 largest responses',
          rule?.top_largest_responses,
          'size_bytes',
          toHumanBytes,
          'No responses.'
        ));
        rankings.appendChild(makeRanking(
          '10 smallest responses',
          rule?.top_smallest_responses,
          'size_bytes',
          toHumanBytes,
          'No responses.'
        ));
        card.appendChild(rankings);
        host.appendChild(card);
      }
    }

    function linePath(points, width, height, pad) {
      if (!points.length) return '';
      const min = Math.min(...points);
      const max = Math.max(...points);
      const span = Math.max(1, max - min);
      const xStep = points.length > 1 ? (width - pad * 2) / (points.length - 1) : 0;
      let d = '';
      for (let i = 0; i < points.length; i++) {
        const x = pad + i * xStep;
        const y = height - pad - ((points[i] - min) / span) * (height - pad * 2);
        d += (i === 0 ? 'M' : 'L') + x.toFixed(2) + ' ' + y.toFixed(2) + ' ';
      }
      return d.trim();
    }

    function renderChart(containerId, points, valueFmt) {
      const host = byId(containerId);
      if (!host) return;
      const width = host.clientWidth > 40 ? host.clientWidth : 320;
      const height = 92;
      const pad = 8;
      const path = linePath(points, width, height, pad);
      const latest = points.length ? points[points.length - 1] : 0;
      const latestText = valueFmt(latest);

      host.innerHTML = [
        '<div>Latest: <strong>' + latestText + '</strong></div>',
        '<svg viewBox="0 0 ' + width + ' ' + height + '" preserveAspectRatio="none" aria-hidden="true">',
        '<path d="' + path + '" stroke="#136f63" stroke-width="2" fill="none" />',
        '</svg>'
      ].join('');
    }

    function renderMetrics(payload) {
      byId('m-urls').textContent = toNum(payload?.cache?.urls_total);
      byId('m-size').textContent = toHumanBytes(payload?.cache?.responses_size_bytes_total);
      byId('m-rss').textContent = toHumanBytes(payload?.memory?.rss_bytes);
      byId('m-alloc').textContent = toHumanBytes(payload?.memory?.go_alloc_bytes);
      byId('m-refresh-avg').textContent = toNum(payload?.refresh_duration_ms?.avg) + ' ms';
      byId('m-crawl').textContent = toNum(payload?.sitemap?.crawl_percentage).toFixed(1) + '%';

      history.push({
        t: Date.now(),
        urls: toNum(payload?.cache?.urls_total),
        rss: toNum(payload?.memory?.rss_bytes),
        refreshAvg: toNum(payload?.refresh_duration_ms?.avg),
      });
      if (history.length > cfg.maxPoints) {
        history.splice(0, history.length - cfg.maxPoints);
      }

      renderChart('c-urls', history.map((x) => x.urls), (v) => String(v));
      renderChart('c-rss', history.map((x) => x.rss), (v) => toHumanBytes(v));
      renderChart('c-refresh', history.map((x) => x.refreshAvg), (v) => String(v) + ' ms');
      renderRules(payload);
    }

    async function refreshStats() {
      try {
        const res = await fetch('/wait0/dashboard/stats', {
          method: 'GET',
          headers: { 'Accept': 'application/json' },
          cache: 'no-store',
        });
        if (!res.ok) {
          throw new Error('stats request failed: ' + res.status);
        }
        const data = await res.json();
        renderMetrics(data);
      } catch (err) {
        console.error(err);
      }
    }

    async function submitInvalidate(ev) {
      ev.preventDefault();
      setInvalidateStatus('status muted', 'Submitting...', false);

      const payload = {
        paths: splitList(byId('paths').value),
        tags: splitList(byId('tags').value),
      };

      try {
        const res = await fetch('/wait0/dashboard/invalidate', {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            'X-Wait0-CSRF': cfg.csrfToken,
          },
          body: JSON.stringify(payload),
        });
        const body = await res.json().catch(() => ({}));
        if (res.ok) {
          setInvalidateStatus('status ok', JSON.stringify(body, null, 2), true);
          refreshStats();
          return;
        }
        setInvalidateStatus('status err', JSON.stringify(body, null, 2), true);
      } catch (err) {
        setInvalidateStatus('status err', String(err), true);
      }
    }

    function setInvalidateStatus(className, text, autoClear) {
      const status = byId('invalidate-status');
      if (!status) return;
      if (invalidateStatusTimer) {
        clearTimeout(invalidateStatusTimer);
        invalidateStatusTimer = null;
      }
      status.className = className;
      status.textContent = text;
      if (!autoClear) return;
      invalidateStatusTimer = setTimeout(() => {
        status.className = 'status muted';
        status.textContent = '';
        invalidateStatusTimer = null;
      }, cfg.invalidateStatusClearMs);
    }

    function initInvalidationForm() {
      const note = byId('invalidate-note');
      const form = byId('invalidate-form');
      const btn = byId('invalidate-btn');
      if (!cfg.invalidationEnabled) {
        btn.disabled = true;
        byId('paths').disabled = true;
        byId('tags').disabled = true;
        note.textContent = 'Invalidation is disabled: no token with scope invalidation:write.';
        return;
      }
      form.addEventListener('submit', submitInvalidate);
      note.textContent = 'This action enqueues asynchronous invalidation.';
    }

    byId('poll-ms').textContent = String(cfg.pollMs);
    initInvalidationForm();
    refreshStats();
    setInterval(refreshStats, cfg.pollMs);
  </script>
</body>
</html>`))
