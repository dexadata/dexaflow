(function () {
  'use strict';
  // The API tokens screen (#1886). It reads and revokes through the same
  // routes as the CLI (GET and DELETE /api/v2/apiTokens, ADR 0074), with the
  // browser's session cookie, so the server's checks are the only checks:
  // a user sees its own tokens, an account manager the tenant's. The page
  // never receives a token or its hash, only metadata, and writes every
  // value with textContent.
  var API = '/api/v2/apiTokens';
  var PAGE = 50;
  var offset = 0;
  var total = 0;
  var pending = '';

  var rows = document.getElementById('rows');
  var table = document.getElementById('tokens');
  var empty = document.getElementById('empty');
  var status = document.getElementById('status');
  var pager = document.getElementById('pager');
  var range = document.getElementById('range');
  var prev = document.getElementById('prev');
  var next = document.getElementById('next');

  // The screen follows the Airflow UI's color mode: the "dark" class the UI
  // sets on its <html> when framed, else the mode it stores ("theme", read
  // by next-themes), else the system setting. It follows a change made in
  // the UI's Appearance menu while the screen is open.
  function dark() {
    try {
      if (window.parent !== window) return window.parent.document.documentElement.classList.contains('dark');
    } catch (e) { /* not same-origin: fall through */ }
    try {
      var mode = window.localStorage.getItem('theme');
      if (mode === 'dark' || mode === 'light') return mode === 'dark';
    } catch (e) { /* storage blocked */ }
    return !!(window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches);
  }
  function applyMode() { document.documentElement.classList.toggle('dark', dark()); }
  applyMode();
  try {
    if (window.parent !== window && window.MutationObserver) {
      new MutationObserver(applyMode).observe(window.parent.document.documentElement, { attributes: true, attributeFilter: ['class'] });
    }
  } catch (e) { /* not same-origin: keep the mode read at load */ }

  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined) n.textContent = text;
    return n;
  }

  function say(text, kind) {
    status.textContent = text;
    status.className = kind || '';
  }

  function when(iso) {
    if (!iso) return '';
    var d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
  }

  // scopeLabel names a token's scope the way the CLI does: deploy or read.
  function scopeLabel(scopes) {
    var s = (scopes || []).map(function (x) { return String(x).replace(/^dexaflow:/, ''); });
    return s.join(', ');
  }

  // problem reads the detail of an RFC 7807 answer, or a fallback.
  function problem(res, fallback) {
    return res.json().then(function (b) {
      return (b && (b.detail || b.title)) || fallback;
    }, function () { return fallback; });
  }

  function refused(res, what) {
    if (res.status === 401) {
      say('Your session has ended. Reload the page to sign in again.', 'error');
      return Promise.resolve();
    }
    return problem(res, what + ' failed (HTTP ' + res.status + ').').then(function (d) { say(d, 'error'); });
  }

  function timeCell(iso, fallback) {
    var td = el('td', 'time', iso ? when(iso) : fallback);
    if (iso) td.title = iso;
    return td;
  }

  function row(t) {
    var tr = el('tr');
    tr.dataset.id = t.id;
    tr.appendChild(el('td', 'name', t.name));
    tr.appendChild(el('td', null, scopeLabel(t.scopes)));
    tr.appendChild(el('td', null, t.owner_email || ''));
    tr.appendChild(timeCell(t.created_at, ''));
    var exp = timeCell(t.expires_at, '');
    if (t.expires_at && new Date(t.expires_at).getTime() <= Date.now()) {
      exp.appendChild(el('span', 'badge', 'Expired'));
    }
    tr.appendChild(exp);
    tr.appendChild(timeCell(t.last_used_at, 'Never'));
    var act = el('td', 'actions');
    if (pending === t.id) {
      act.appendChild(el('span', 'confirm', 'Anything using it stops working.'));
      var yes = el('button', 'danger', 'Revoke');
      yes.type = 'button';
      yes.setAttribute('aria-label', 'Confirm revoking ' + t.name);
      yes.addEventListener('click', function () { revoke(t); });
      var no = el('button', 'secondary', 'Cancel');
      no.type = 'button';
      no.addEventListener('click', function () { pending = ''; render(last); });
      act.appendChild(yes);
      act.appendChild(no);
    } else {
      var b = el('button', 'secondary', 'Revoke');
      b.type = 'button';
      b.setAttribute('aria-label', 'Revoke ' + t.name);
      b.addEventListener('click', function () { pending = t.id; say(''); render(last); });
      act.appendChild(b);
    }
    tr.appendChild(act);
    return tr;
  }

  var last = [];

  function render(tokens) {
    last = tokens;
    while (rows.firstChild) rows.removeChild(rows.firstChild);
    tokens.forEach(function (t) { rows.appendChild(row(t)); });
    table.hidden = tokens.length === 0;
    empty.hidden = tokens.length !== 0;
    pager.hidden = total <= PAGE;
    range.textContent = tokens.length ? (offset + 1) + ' to ' + (offset + tokens.length) + ' of ' + total : '';
    prev.disabled = offset === 0;
    next.disabled = offset + tokens.length >= total;
  }

  function load() {
    return fetch(API + '?limit=' + PAGE + '&offset=' + offset, {
      credentials: 'same-origin', headers: { Accept: 'application/json' },
    }).then(function (res) {
      if (!res.ok) return refused(res, 'Loading the tokens');
      return res.json().then(function (body) {
        total = body.total_entries || 0;
        var tokens = body.api_tokens || [];
        if (tokens.length === 0 && offset > 0) {
          offset = Math.max(0, offset - PAGE);
          return load();
        }
        render(tokens);
      });
    }, function () { say('The server could not be reached.', 'error'); });
  }

  function revoke(t) {
    return fetch(API + '/' + encodeURIComponent(t.id), {
      method: 'DELETE', credentials: 'same-origin', headers: { Accept: 'application/json' },
    }).then(function (res) {
      pending = '';
      if (res.status === 204) {
        say('Revoked ' + t.name + '.', 'ok');
        return load();
      }
      if (res.status === 404) {
        say(t.name + ' was already revoked or is not yours to revoke.', 'error');
        return load();
      }
      return refused(res, 'Revoking ' + t.name).then(load);
    }, function () { say('The server could not be reached.', 'error'); });
  }

  prev.addEventListener('click', function () { offset = Math.max(0, offset - PAGE); pending = ''; load(); });
  next.addEventListener('click', function () { offset += PAGE; pending = ''; load(); });
  load();
})();
