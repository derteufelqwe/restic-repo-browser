/* Client-side behaviour that htmx does not cover: Chart.js charts, menus, tabs,
   toasts, the repository dialog, the cleanup schedule controls and the snapshot
   explorer's selection, filter and sort. All data arrives as server-rendered
   HTML; charts read JSON from data attributes. */
(function () {
  'use strict';

  const $ = (s, r = document) => r.querySelector(s);
  const $$ = (s, r = document) => Array.from(r.querySelectorAll(s));
  const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  const GIB = 1073741824, MIB = 1048576;

  /* ---------- formatting ---------- */

  const num = (v, d = 0) => v.toLocaleString('en-US', { minimumFractionDigits: d, maximumFractionDigits: d });
  function fmtBytes(b) {
    if (b >= GIB) return num(b / GIB, 1) + ' GB';
    if (b >= MIB) { const v = b / MIB; return num(v, v < 10 ? 1 : 0) + ' MB'; }
    if (b >= 1024) return num(b / 1024) + ' KB';
    return b + ' B';
  }
  function fmtDate(ms) {
    const d = new Date(ms), now = new Date();
    const y = d.getFullYear() !== now.getFullYear() ? ', ' + d.getFullYear() : '';
    return `${MONTHS[d.getMonth()]} ${d.getDate()}${y}`;
  }
  function fmtDateTime(ms) {
    const d = new Date(ms), p = (n) => String(n).padStart(2, '0');
    return `${fmtDate(ms)}, ${p(d.getHours())}:${p(d.getMinutes())}`;
  }
  const readJSON = (el, attr) => { try { return JSON.parse(el.getAttribute(attr)); } catch (e) { return null; } };

  /* ---------- toasts ---------- */

  function toast(t) {
    const box = $('#toasts');
    if (!box || !t) return;
    const el = document.createElement('div');
    el.className = 'toast' + (t.done ? ' done' : '') + (t.fail ? ' fail' : '');
    const b = document.createElement('b'); b.textContent = t.title; el.appendChild(b);
    if (t.sub) { const s = document.createElement('small'); s.textContent = t.sub; el.appendChild(s); }
    box.appendChild(el);
    setTimeout(() => el.remove(), t.fail ? 6500 : 4200);
  }
  document.body.addEventListener('toast', (e) => toast(e.detail));

  /* ---------- charts ---------- */

  const COLORS = { bar: '#4f7b80', barHover: '#2e5a61', ochre: '#b8822a', line: '#15343a', kept: '#2e5a61', gone: 'rgba(158, 43, 58, 0.4)', grid: 'rgba(21, 52, 58, 0.12)', text: '#4b5b5c' };
  let defaultsDone = false;
  function chartDefaults() {
    if (!window.Chart || defaultsDone) return;
    defaultsDone = true;
    const C = Chart.defaults;
    C.font.family = getComputedStyle(document.body).fontFamily;
    C.font.size = 12; C.color = COLORS.text; C.borderColor = COLORS.grid; C.animation = false;
    const t = C.plugins.tooltip;
    t.backgroundColor = '#15343a'; t.padding = 10; t.cornerRadius = 4; t.boxPadding = 4;
    t.titleFont = { weight: '600', size: 13 }; t.bodyFont = { size: 12 }; t.footerFont = { weight: '400', size: 12 };
  }

  function historyChart(canvas, d) {
    const n = d.keys.length;
    let selected = null;
    const colors = () => d.keys.map((k) => (k === selected ? COLORS.ochre : COLORS.bar));
    const hover = () => d.keys.map((k) => (k === selected ? COLORS.ochre : COLORS.barHover));
    const sizes = d.sizes.map((b) => b / GIB);
    const top = Math.max(...sizes);
    const digits = top < 0.1 ? 3 : top < 1 ? 2 : top < 10 ? 1 : 0;
    const inMB = Math.max(...d.added) < GIB;
    const unit = inMB ? 'MB' : 'GB', div = inMB ? MIB : GIB;
    const chart = new Chart(canvas, {
      data: {
        labels: d.short,
        datasets: [
          { type: 'bar', label: 'Data added', data: d.added.map((b) => b / div), backgroundColor: colors(), hoverBackgroundColor: hover(), yAxisID: 'y', order: 2, borderRadius: 1, maxBarThickness: 30 },
          { type: 'line', label: 'Snapshot size', data: sizes, borderColor: COLORS.line, backgroundColor: COLORS.line, borderWidth: 2, pointRadius: n > 45 ? 0 : 3, pointHoverRadius: 4, yAxisID: 'y1', order: 1, tension: 0 },
        ],
      },
      options: {
        responsive: true, maintainAspectRatio: false, interaction: { mode: 'index', intersect: false },
        scales: {
          x: { grid: { display: false }, ticks: { maxRotation: 0, autoSkip: true, maxTicksLimit: 14 } },
          y: { beginAtZero: true, title: { display: true, text: `Data added (${unit})` }, ticks: { callback: (v) => num(v, 1).replace(/\.0$/, '') }, grid: { color: COLORS.grid } },
          y1: { position: 'right', beginAtZero: true, title: { display: true, text: 'Snapshot size (GB)' }, grid: { display: false }, ticks: { callback: (v) => num(v, digits) } },
        },
        plugins: {
          legend: { position: 'top', align: 'end', labels: { usePointStyle: true, boxWidth: 8, boxHeight: 8, padding: 16 } },
          tooltip: {
            callbacks: {
              title: (items) => d.titles[items[0].dataIndex],
              label: (c) => (c.datasetIndex === 0 ? 'Data added: ' + fmtBytes(d.added[c.dataIndex]) : 'Snapshot size: ' + fmtBytes(d.sizes[c.dataIndex])),
              footer: (items) => { const k = d.counts[items[0].dataIndex]; return `${k} snapshot${k > 1 ? 's' : ''}`; },
            },
          },
        },
        onClick: (e, _els, ch) => {
          const pts = ch.getElementsAtEventForMode(e.native, 'index', { intersect: false }, true);
          if (pts.length) selectBucket(canvas, d, pts[0].index);
        },
        onHover: (e, els, ch) => { ch.canvas.style.cursor = els.length ? 'pointer' : 'default'; },
      },
    });
    canvas._setSelected = (key) => {
      selected = key;
      chart.data.datasets[0].backgroundColor = colors();
      chart.data.datasets[0].hoverBackgroundColor = hover();
      chart.update('none');
    };
    canvas._selected = () => selected;
    return chart;
  }

  // A click on a bar filters the snapshot list to that period.
  function selectBucket(canvas, d, i) {
    const form = $('#snap-filter');
    if (!form) return;
    const same = canvas._selected && canvas._selected() === d.keys[i];
    if (same) { clearBucket(); return; }
    canvas._setSelected(d.keys[i]);
    setFilter(form, d.from[i], d.to[i], d.titles[i]);
    const panel = $('#panel-snapshots');
    if (panel) panel.scrollIntoView({ behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth', block: 'start' });
  }
  function setFilter(form, from, to, label) {
    form.elements.from.value = from; form.elements.to.value = to; form.elements.label.value = label;
    htmx.trigger(form, 'bucket-changed');
  }
  function clearBucket() {
    const form = $('#snap-filter');
    if (!form) return;
    setFilter(form, '', '', '');
    const c = $('#chart-panel canvas');
    if (c && c._setSelected) c._setSelected(null);
  }

  // The sparkline canvas is only 34px high, so its tooltip is drawn as a separate
  // element that can extend beyond the canvas.
  let sparkTip = null;
  function sparkTooltip(ctx, d) {
    if (!sparkTip) {
      sparkTip = document.createElement('div');
      sparkTip.className = 'chart-tip';
      sparkTip.hidden = true;
      document.body.appendChild(sparkTip);
    }
    const tip = ctx.tooltip;
    if (tip.opacity === 0 || !tip.dataPoints || !tip.dataPoints.length) { sparkTip.hidden = true; return; }
    const i = tip.dataPoints[0].dataIndex;
    sparkTip.textContent = '';
    const title = document.createElement('b'); title.textContent = fmtDateTime(d.t[i]);
    const added = document.createElement('span'); added.textContent = 'Data added: ' + fmtBytes(d.a[i]);
    sparkTip.append(title, added);
    if (d.s && d.s[i]) { const id = document.createElement('span'); id.textContent = 'Snapshot ' + d.s[i]; sparkTip.appendChild(id); }
    const box = ctx.chart.canvas.getBoundingClientRect();
    sparkTip.style.left = box.left + tip.caretX + 'px';
    sparkTip.style.top = box.top + 'px';
    sparkTip.hidden = false;
  }

  function sparkline(canvas, d) {
    if (d.t.length < 2) return;
    new Chart(canvas, {
      type: 'bar',
      data: { labels: d.t, datasets: [{ data: d.a, backgroundColor: canvas.hasAttribute('data-muted') ? '#a5bfbd' : COLORS.bar, hoverBackgroundColor: COLORS.barHover, borderRadius: 1, barPercentage: 0.85, categoryPercentage: 1 }] },
      options: {
        responsive: true, maintainAspectRatio: false, layout: { padding: 0 },
        interaction: { mode: 'index', intersect: false },
        scales: { x: { display: false }, y: { display: false, beginAtZero: true } },
        plugins: { legend: { display: false }, tooltip: { enabled: false, external: (ctx) => sparkTooltip(ctx, d) } },
      },
    });
  }

  function retentionChart(canvas, d) {
    new Chart(canvas, {
      type: 'bar',
      data: { labels: d.labels, datasets: [
        { label: 'Kept', data: d.kept, backgroundColor: COLORS.kept, borderRadius: 1 },
        { label: 'Forgotten', data: d.gone, backgroundColor: COLORS.gone, borderRadius: 1 },
      ] },
      options: {
        responsive: true, maintainAspectRatio: false, interaction: { mode: 'index', intersect: false },
        scales: {
          x: { stacked: true, grid: { display: false }, ticks: { maxRotation: 0, autoSkip: true, maxTicksLimit: 5 } },
          y: { stacked: true, beginAtZero: true, title: { display: true, text: `Snapshots per ${d.unit}` }, ticks: { precision: 0 }, grid: { color: COLORS.grid } },
        },
        plugins: { legend: { position: 'top', align: 'start', labels: { usePointStyle: true, boxWidth: 8, boxHeight: 8, padding: 14 } }, tooltip: { callbacks: { title: (i) => d.titles[i[0].dataIndex] } } },
      },
    });
  }

  function initCharts(root) {
    if (!window.Chart) return;
    chartDefaults();
    const make = (sel, attr, fn) => $$(sel, root).forEach((c) => {
      const d = readJSON(c, attr);
      if (!d) return;
      const old = Chart.getChart(c);
      if (old) old.destroy();
      fn(c, d);
    });
    make('canvas[data-history]', 'data-history', (c, d) => { if (d.keys && d.keys.length) historyChart(c, d); });
    make('canvas[data-spark]', 'data-spark', sparkline);
    make('canvas[data-retention]', 'data-retention', retentionChart);
  }
  document.body.addEventListener('htmx:beforeCleanupElement', (e) => {
    const el = e.detail.elt;
    if (el.tagName === 'CANVAS' && window.Chart) { const c = Chart.getChart(el); if (c) c.destroy(); }
  });

  /* ---------- tabs (repository page) ---------- */

  function initTabs(root) {
    $$('[role="tablist"]', root).forEach((list) => {
      if (list._ready) return;
      list._ready = true;
      const btns = $$('[role="tab"]', list);
      const select = (b, focus) => {
        btns.forEach((x) => {
          const on = x === b;
          x.setAttribute('aria-selected', on); x.tabIndex = on ? 0 : -1;
          const p = document.getElementById(x.getAttribute('aria-controls'));
          if (p) p.hidden = !on;
        });
        if (focus) b.focus();
        try { history.replaceState(null, '', location.pathname + location.search + '#' + b.dataset.tab); } catch (e) { /* ignore */ }
      };
      btns.forEach((b, i) => {
        b.addEventListener('click', () => select(b));
        b.addEventListener('keydown', (e) => {
          if (e.key === 'ArrowRight') select(btns[(i + 1) % btns.length], true);
          if (e.key === 'ArrowLeft') select(btns[(i - 1 + btns.length) % btns.length], true);
        });
      });
      select(btns.find((b) => '#' + b.dataset.tab === location.hash) || btns[0]);
    });
  }

  /* ---------- menus ---------- */

  const closeMenus = () => $$('.menu').forEach((m) => (m.hidden = true));
  document.addEventListener('click', (e) => {
    const btn = e.target.closest('[data-menu]');
    $$('.menu').forEach((m) => { if (!btn || m !== btn.nextElementSibling) m.hidden = true; });
    if (!btn) return;
    const m = btn.nextElementSibling;
    m.hidden = !m.hidden; btn.setAttribute('aria-expanded', String(!m.hidden));
    if (!m.hidden) { // fixed position so scrolling table wrappers never clip the menu
      const r = btn.getBoundingClientRect();
      m.style.top = Math.min(r.bottom + 4, innerHeight - m.offsetHeight - 8) + 'px';
      m.style.right = Math.max(8, innerWidth - r.right) + 'px';
    }
  });
  window.addEventListener('scroll', closeMenus, true);
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape') { closeMenus(); closeDrawer(); } });

  /* ---------- overview cards ---------- */

  document.addEventListener('click', (e) => {
    const card = e.target.closest('.repo-card');
    if (card && !e.target.closest('a')) { const a = $('a', card); if (a) a.click(); }
  });

  /* ---------- snapshot list: collapse the expanded paths ---------- */

  document.addEventListener('click', (e) => {
    const less = e.target.closest('[data-collapse]');
    if (less) less.closest('details').open = false;
  });

  /* ---------- snapshot filter chip ---------- */

  document.addEventListener('click', (e) => { if (e.target.closest('[data-clear-bucket]')) clearBucket(); });
  document.body.addEventListener('htmx:afterSwap', (e) => {
    // A new chart range invalidates the selected period.
    if (e.detail.target && e.detail.target.id === 'chart-panel') {
      const form = $('#snap-filter');
      if (form && form.elements.from.value) setFilter(form, '', '', '');
    }
  });

  /* ---------- repository dialog (settings) ---------- */

  document.body.addEventListener('htmx:afterSwap', (e) => {
    if (e.detail.target && e.detail.target.id === 'dlg-content') {
      const dlg = $('#repo-dialog');
      if (dlg && !dlg.open) dlg.showModal();
    }
  });
  document.addEventListener('click', (e) => {
    if (e.target.closest('[data-close-dialog]')) { const dlg = $('#repo-dialog'); if (dlg) dlg.close(); }
  });

  /* ---------- cleanup tab ---------- */

  document.body.addEventListener('htmx:afterSwap', (e) => {
    const t = e.detail.target;
    if (!t || t.id !== 'cleanup-body') return;
    const view = $('[data-tab-label]', t);
    if (!view) return;
    const count = $('#tab-cleanup .count');
    if (count) count.textContent = view.dataset.tabLabel;
    const next = $('[data-fact="next-cleanup"]');
    if (next && view.dataset.nextRun) next.textContent = view.dataset.nextRun;
  });

  /* ---------- tasks: one at a time, with progress ---------- */

  let lastTaskId = (() => { const b = $('#task-banner'); return b ? b.dataset.lastId : null; })();

  // Buttons that start a task are disabled while one runs.
  function applyBusy() {
    const b = $('#task-banner');
    const running = !!b && b.dataset.running === 'true';
    $$('[data-needs-idle]').forEach((el) => {
      if (el.dataset.title === undefined) el.dataset.title = el.title;
      el.disabled = running || el.hasAttribute('data-force-disabled');
      el.title = running ? 'Wait for the running task to finish.' : el.dataset.title;
    });
  }
  document.body.addEventListener('htmx:afterSwap', (e) => {
    if (!e.detail.target || e.detail.target.id !== 'task-banner') return;
    applyBusy();
    const b = $('#task-banner');
    if (!b || b.dataset.lastId === lastTaskId) return;
    lastTaskId = b.dataset.lastId; // a task finished since the page was loaded
    const editing = $('#cleanup-form') || $('dialog[open]') || $('.browser');
    if (editing) toast({ title: 'Task finished', sub: 'Reload the page to see the new data.', done: true });
    else location.reload();
  });

  // A slim bar at the top shows that a (slow) request is in progress.
  const bar = document.createElement('div');
  bar.className = 'busybar'; bar.hidden = true; bar.setAttribute('aria-hidden', 'true');
  document.body.appendChild(bar);
  let inflight = 0, barTimer = null;
  const quiet = (e) => { const id = e.detail.elt && e.detail.elt.id; return id === 'task-banner' || id === 'cleanup-runs'; }; // background polling
  document.body.addEventListener('htmx:beforeRequest', (e) => {
    if (quiet(e)) return;
    if (++inflight === 1) barTimer = setTimeout(() => { bar.hidden = false; }, 200);
  });
  document.body.addEventListener('htmx:afterRequest', (e) => {
    if (quiet(e)) return;
    if (--inflight <= 0) { inflight = 0; clearTimeout(barTimer); bar.hidden = true; }
  });

  // Downloads start only after restic has opened the repository; say so.
  const downloadNotice = () => toast({ title: 'Preparing the download…', sub: 'Your browser starts saving as soon as restic delivers data.' });
  document.addEventListener('click', (e) => { if (e.target.closest('a[href*="/download?"]')) downloadNotice(); });
  document.addEventListener('submit', (e) => { if (e.target.id === 'sel-form') downloadNotice(); });

  /* ---------- folder picker (repository path) ---------- */

  function closePicker() { const p = $('#fs-picker'); if (p) p.outerHTML = '<div id="fs-picker"></div>'; }
  document.addEventListener('click', (e) => {
    const pick = e.target.closest('[data-fs-pick]');
    if (pick) { const input = $('#r-path'); input.value = pick.dataset.fsPick; closePicker(); input.focus(); }
    if (e.target.closest('[data-fs-close]')) closePicker();
  });

  /* ---------- snapshot explorer ---------- */

  function closeDrawer() {
    const dr = $('#drawer');
    if (dr) { dr.classList.remove('is-open'); dr.setAttribute('aria-hidden', 'true'); }
  }
  document.body.addEventListener('htmx:afterSwap', (e) => {
    const t = e.detail.target;
    if (!t) return;
    if (t.id === 'drawer') {
      t.classList.add('is-open'); t.setAttribute('aria-hidden', 'false');
      const b = $('[data-close-drawer]', t); if (b) b.focus();
    }
    // Only a new folder listing resets the explorer. Other swaps (such as the task
    // banner replacing itself every few seconds) must leave the open drawer alone.
    const path = (e.detail.pathInfo && e.detail.pathInfo.requestPath) || '';
    if (t.id === 'listing-pane' || (t.isConnected === false && path.includes('/listing') && $('#listing-pane'))) {
      const pane = $('#listing-pane');
      if (pane) {
        $$('.tree li').forEach((li) => li.classList.toggle('is-current', li.dataset.path === pane.dataset.path));
        closeDrawer(); updateSelection();
      }
    }
  });
  document.addEventListener('click', (e) => {
    if (e.target.closest('[data-close-drawer]')) closeDrawer();
    const tw = e.target.closest('[data-toggle]');
    if (tw) { const li = tw.closest('li'); li.setAttribute('aria-expanded', li.getAttribute('aria-expanded') === 'true' ? 'false' : 'true'); }
    const clear = e.target.closest('[data-clear-selection]');
    if (clear) { $$('#listing input[name="path"], #sel-all').forEach((c) => (c.checked = false)); updateSelection(); }
    const srt = e.target.closest('[data-sort]');
    if (srt) sortListing(srt);
  });
  document.addEventListener('change', (e) => {
    if (e.target.id === 'sel-all') $$('#listing tbody tr:not([hidden]) input[name="path"]').forEach((c) => (c.checked = e.target.checked));
    if (e.target.id === 'sel-all' || e.target.matches('#listing input[name="path"]')) updateSelection();
  });
  document.addEventListener('input', (e) => {
    if (e.target.id !== 'lf') return;
    const f = e.target.value.toLowerCase();
    $$('#listing tbody tr').forEach((tr) => { tr.hidden = !!f && !tr.dataset.name.toLowerCase().includes(f); });
  });

  function updateSelection() {
    const bar = $('#selbar');
    if (!bar) return;
    const rows = $$('#listing tbody tr'), picked = rows.filter((tr) => $('input[name="path"]', tr).checked);
    rows.forEach((tr) => tr.classList.toggle('is-selected', picked.includes(tr)));
    bar.hidden = picked.length === 0;
    const dirs = picked.filter((tr) => tr.dataset.dir === 'true').length, files = picked.length - dirs;
    const parts = [dirs && `${dirs} folder${dirs > 1 ? 's' : ''}`, files && `${files} file${files > 1 ? 's' : ''}`].filter(Boolean).join(' and ');
    $('#sel-count').textContent = `${picked.length} item${picked.length > 1 ? 's' : ''} selected (${parts})`;
    const all = $('#sel-all');
    if (all) { all.checked = picked.length > 0 && picked.length === rows.filter((r) => !r.hidden).length; all.indeterminate = picked.length > 0 && !all.checked; }
  }

  function sortListing(btn) {
    const key = btn.dataset.sort, th = btn.closest('th'), tbody = $('#listing tbody');
    const dir = th.getAttribute('aria-sort') === 'ascending' ? -1 : 1;
    $$('#listing th').forEach((h) => { h.removeAttribute('aria-sort'); const b = $('.sort', h); if (b) b.removeAttribute('data-dir'); });
    th.setAttribute('aria-sort', dir > 0 ? 'ascending' : 'descending'); btn.dataset.dir = dir > 0 ? 'asc' : 'desc';
    const val = (tr) => (key === 'name' ? tr.dataset.name.toLowerCase() : +tr.dataset[key]);
    $$('tr', tbody).sort((a, b) => {
      const da = a.dataset.dir === 'true', db = b.dataset.dir === 'true';
      if (da !== db) return da ? -1 : 1;
      const x = val(a), y = val(b);
      return (x > y ? 1 : x < y ? -1 : 0) * dir;
    }).forEach((tr) => tbody.appendChild(tr));
  }

  /* ---------- login artwork: a stack of straight layers, newest on top ---------- */

  function loginArt() {
    const art = $('[data-login-art]');
    if (!art) return;
    let s = 7;
    const rnd = () => { s = (s + 0x6D2B79F5) | 0; let t = Math.imul(s ^ (s >>> 15), 1 | s); t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t; return ((t ^ (t >>> 14)) >>> 0) / 4294967296; };
    const tints = ['#2e5a61', '#4f7b80', '#7a9ea0', '#a5bfbd', '#c9d6d3'];
    const mix = (a, b, t) => '#' + [1, 3, 5].map((i) => Math.round(parseInt(a.substr(i, 2), 16) + (parseInt(b.substr(i, 2), 16) - parseInt(a.substr(i, 2), 16)) * t).toString(16).padStart(2, '0')).join('');
    const tint = (t) => { const p = t * (tints.length - 1), i = Math.min(tints.length - 2, Math.floor(p)); return mix(tints[i], tints[i + 1], p - i); };
    const W = 800, H = 900, n = 14, gap = 5;
    const raw = Array.from({ length: n }, () => 28 + rnd() * 70);
    const k = (H - gap * (n - 1)) / raw.reduce((a, b) => a + b, 0);
    let y = 0, svg = `<svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="xMinYMid slice" aria-hidden="true">`;
    raw.forEach((v, i) => {
      const h = v * k, rank = i / (n - 1), w = W * (0.5 + 0.5 * (1 - rank) ** 1.3) - (i % 3) * 14;
      const fill = i === 3 ? '#b8822a' : tint(0.2 + 0.8 * (1 - rank));
      svg += `<rect class="band-anim" style="animation-delay:${((n - 1 - i) * 0.07).toFixed(2)}s" x="0" y="${y.toFixed(1)}" width="${w.toFixed(1)}" height="${h.toFixed(1)}" fill="${fill}"/>`;
      y += h + gap;
    });
    art.insertAdjacentHTML('afterbegin', svg + '</svg>');
  }

  /* ---------- boot ---------- */

  htmx.onLoad((root) => { initCharts(root); initTabs(root); applyBusy(); });
  loginArt();
})();
