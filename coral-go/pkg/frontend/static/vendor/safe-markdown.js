// A small, safe markdown renderer for machine-supplied text (docs/design/saved-expand.md section 3; Security's requirements of 2026-10-21). The text is HOSTILE: an agent or a teammate wrote it. So:
//  * the DOM is built with createElement and textContent ONLY, from a closed list of tags: no HTML-string property, no HTML parser, no eval, no timers, no network;
//  * raw HTML is plain text (every "<" stays a character in a text node);
//  * NO image is ever loaded: an image is its alt text followed by "(image not shown)"; no element with a src exists;
//  * a link is rendered only for http, https and mailto, with no userinfo, as a real <a> with rel="noopener noreferrer", target=_blank and no referrer, and the (punycode) host is shown after its label
//    so a label cannot hide where it goes; anything else is plain text;
//  * control, bidi and zero-width characters are removed from the WHOLE input first, so no text node and no link label can carry one;
//  * the work is BOUNDED, not merely fast: one input cap, a total character-step budget that every scan pays into, a node budget, nesting caps, and table caps. A pathological input (a quarter of a
//    million asterisks, brackets or backticks) ends in a bounded amount of work with the rest shown as plain text and a "shown in part" mark; nothing is quadratic.
// API: AccountMarkdown.render(text, opts) -> { node: HTMLElement, partial: boolean }. opts.linkPolicy(url) is the host's optional link callback (see resolveLink). It never throws; on an internal fault it returns the cleaned text in a pre-wrap block.
(function () {
  'use strict';

  var MAX_INPUT = 256 * 1024;       // characters read at all
  var MAX_STEPS = 3 * 1000 * 1000;  // character steps all scans together may spend
  var MAX_NODES = 4000;             // elements and text nodes built
  var MAX_DEPTH = 6;                // nesting of quotes and inline emphasis
  var MAX_LIST_DEPTH = 4;
  var MAX_TABLE_ROWS = 60, MAX_TABLE_COLS = 12;
  var MAX_FENCE_LINES = 2000;
  var MAX_URL = 2048;
  var MAX_LINE = 20000;             // characters of one line that any pattern is ever run on; the rest is dropped and the result is marked partial
  var ALLOWED = { div: 1, p: 1, pre: 1, code: 1, ul: 1, ol: 1, li: 1, blockquote: 1, table: 1, thead: 1, tbody: 1, tr: 1, th: 1, td: 1, a: 1, strong: 1, em: 1, br: 1, hr: 1, span: 1 };

  // every control character but newline and tab, DEL and the C1 controls, the soft hyphen, the combining grapheme joiner, the Mongolian vowel separator, bidi controls and isolates, zero-width characters,
  // the line and paragraph separators, the byte order mark and the tag characters. NOT stripped: U+FE0F (emoji presentation), U+200D (the emoji joiner) and U+200C (ZWNJ, part of Persian, Kurdish and Indic spelling). Written as escapes: no invisible literal is in this file.
  var STRIP = /[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F-\u009F\u00AD\u034F\u061C\u180E\u200B\u200E\u200F\u2028\u2029\u202A-\u202E\u2060-\u2064\u2066-\u2069\uFEFF\u{E0000}-\u{E007F}]/gu;
  // trimRight removes trailing spaces and tabs with a scan back from the end: LINEAR. (A trailing-blank regex is quadratic on a long run of spaces that is not at the end: 25 s of freeze on 256 KiB.)
  function trimRight(s) { var e = s.length; while (e > 0 && (s.charCodeAt(e - 1) === 32 || s.charCodeAt(e - 1) === 9)) { e--; } return e === s.length ? s : s.substring(0, e); }
  // stripClosingHashes: "# Title ##" -> "Title": trailing hashes count only after a space, found by scanning back (linear, no regex)
  function stripClosingHashes(s) {
    var t = trimRight(s), e = t.length;
    while (e > 0 && t.charCodeAt(e - 1) === 35) { e--; }
    if (e < t.length && e > 0 && (t.charCodeAt(e - 1) === 32 || t.charCodeAt(e - 1) === 9)) { return trimRight(t.substring(0, e)); }
    return t;
  }
  // isSepLine: a table separator row ("|---|:--:|"): blanks removed once, then one pattern with no nested ambiguity
  function isSepLine(line) {
    if (line.length > MAX_LINE || line.indexOf('-') === -1) { return false; }
    return /^\|?:?-+:?(?:\|:?-+:?)*\|?$/.test(line.replace(/[ \t]/g, ''));
  }
  function clean(s) { return String(s).replace(/\r\n?/g, '\n').replace(STRIP, ''); }

  function Over() { this.over = true; }

  function Ctx(policy) { this.policy = policy; this.steps = 0; this.nodes = 0; this.partial = false; }
  Ctx.prototype.pay = function (n) { this.steps += n; if (this.steps > MAX_STEPS) { throw new Over(); } };
  Ctx.prototype.mk = function (tag, cls) {
    if (!ALLOWED[tag]) { throw new Error('tag'); }
    if (++this.nodes > MAX_NODES) { throw new Over(); }
    var e = document.createElement(tag);
    if (cls) { e.className = cls; }
    return e;
  };
  Ctx.prototype.text = function (parent, s) {
    if (s === '') { return; }
    if (++this.nodes > MAX_NODES) { throw new Over(); }
    var last = parent.lastChild;
    if (last && last.nodeType === 3) { last.appendData(s); } else { parent.appendChild(document.createTextNode(s)); }
  };

  // ---- links ----
  // safeLink returns {href, shown} for an http, https or mailto link without userinfo, otherwise null (the caller shows the source text as plain text)
  function safeLink(raw) {
    var s = raw.trim();
    if (s.length === 0 || s.length > MAX_URL || /\s/.test(s)) { return null; }
    var u;
    try { u = new URL(s); } catch (e) { return null; }
    if (u.protocol === 'http:' || u.protocol === 'https:') {
      if (u.username !== '' || u.password !== '' || u.hostname === '') { return null; }
      return { href: u.href, shown: u.hostname }; // the hostname is the punycode (ASCII) form of an international name
    }
    if (u.protocol === 'mailto:') {
      var addr = u.pathname;
      if (addr === '' || addr.length > 254 || u.hostname !== '' || u.username !== '') { return null; }
      return { href: u.href, shown: addr };
    }
    return null;
  }

  // resolveLink: the host may supply opts.linkPolicy(url) to own the links of ITS pages (an artifact, a saved item). It returns undefined/null to leave the url to the default policy above, false to refuse it
  // (plain text), or { href, shown } for a link it owns. The renderer adds NO scheme: an owned href is either a same-origin absolute path ("/x", never "//" or a backslash or a blank) or must itself
  // pass safeLink; the host's { internal: true } only drops target=_blank. A policy that throws, or returns anything else, is a refusal.
  function resolveLink(ctx, raw) {
    if (!ctx.policy) { return safeLink(raw); }
    var r;
    try { r = ctx.policy(raw); } catch (e) { return null; }
    if (r === undefined || r === null) { return safeLink(raw); }
    if (!r || typeof r !== 'object' || typeof r.href !== 'string' || typeof r.shown !== 'string') { return null; }
    var h = r.href;
    if (h.length === 0 || h.length > MAX_URL || r.shown.length > 254) { return null; }
    if (h.charAt(0) === '/') {
      if (h.charAt(1) === '/' || /[\\\s\u0000-\u001F\u007F]/.test(h)) { return null; }
      return { href: h, shown: clean(r.shown), internal: r.internal === true };
    }
    var d = safeLink(h);
    return d ? { href: d.href, shown: clean(r.shown), internal: false } : null;
  }

  // ---- inline ----
  // indexOfBounded: find needle from i, paying for what it scans; a run that is not found costs only the distance to the end of THIS inline segment, which the caller bounds.
  function find(ctx, s, needle, from, end) {
    var j = s.indexOf(needle, from);
    if (j === -1 || j >= end) { ctx.pay(end - from + 1); return -1; }
    ctx.pay(j - from + 1);
    return j;
  }
  function inline(ctx, parent, s, depth, inLink) {
    var i = 0, n = s.length, buf = '';
    function flush() { if (buf !== '') { ctx.text(parent, buf); buf = ''; } }
    while (i < n) {
      var c = s.charAt(i);
      ctx.pay(1);
      if (c === '\\' && i + 1 < n && '\\`*_[]()!#|>~-.+'.indexOf(s.charAt(i + 1)) !== -1) { buf += s.charAt(i + 1); i += 2; continue; }
      if (c === '`') {
        var run = 1; while (i + run < n && s.charAt(i + run) === '`') { run++; }
        var fence = s.substr(i, run);
        var close = find(ctx, s, fence, i + run, n);
        // a closing run must be exactly as long as the opening one
        while (close !== -1 && (s.charAt(close + run) === '`' || s.charAt(close - 1) === '`')) { close = find(ctx, s, fence, close + 1, n); }
        if (close !== -1) {
          flush();
          var code = ctx.mk('code');
          ctx.text(code, s.substring(i + run, close).replace(/\n/g, ' '));
          parent.appendChild(code);
          i = close + run; continue;
        }
        buf += fence; i += run; continue;
      }
      if (c === '!' && s.charAt(i + 1) === '[') {
        var im = linkParts(ctx, s, i + 1);
        if (im) {
          flush();
          // an image is never loaded: its alt text and a plain note
          var sp = ctx.mk('span', 'md-img');
          ctx.text(sp, (im.label || 'image') + ' (image not shown)');
          parent.appendChild(sp);
          i = im.end; continue;
        }
      }
      if (c === '[' && inLink) { buf += c; i++; continue; } // a link inside a link label is plain text (a link in a link is not valid and would hide the real target)
      if (c === '[') {
        var lk = linkParts(ctx, s, i);
        if (lk) {
          var safe = resolveLink(ctx, lk.url);
          if (safe && depth < MAX_DEPTH) {
            flush();
            var a = ctx.mk('a');
            a.setAttribute('href', safe.href);
            a.setAttribute('rel', 'noopener noreferrer');
            if (!safe.internal) { a.setAttribute('target', '_blank'); }
            a.setAttribute('referrerpolicy', 'no-referrer');
            inline(ctx, a, lk.label, depth + 1, true);
            parent.appendChild(a);
            var host = ctx.mk('span', 'md-host');
            ctx.text(host, ' (' + safe.shown + ')');
            parent.appendChild(host);
            i = lk.end; continue;
          }
          // not a link we may render: its source text stays as plain text
          buf += s.substring(i, lk.end); i = lk.end; continue;
        }
      }
      if ((c === '*' || c === '_') && depth < MAX_DEPTH) {
        var dbl = s.charAt(i + 1) === c;
        var mark = dbl ? c + c : c;
        var open = i + mark.length;
        var cl = find(ctx, s, mark, open, n);
        // an underscore inside a word (window.__pwn, snake_case_name) is not emphasis, as in CommonMark: it must open after a non-word character and close before one
        var word = /[A-Za-z0-9]/;
        var intra = c === '_' && ((i > 0 && word.test(s.charAt(i - 1))) || (cl !== -1 && word.test(s.charAt(cl + mark.length) || ' ')));
        if (cl > open && s.charAt(open) !== ' ' && !intra) {
          flush();
          var em = ctx.mk(dbl ? 'strong' : 'em');
          inline(ctx, em, s.substring(open, cl), depth + 1, inLink);
          parent.appendChild(em);
          i = cl + mark.length; continue;
        }
        buf += mark; i += mark.length; continue;
      }
      buf += c; i++;
    }
    flush();
  }
  // linkParts parses "[label](url ...)" starting at s[i] === "[": the label may hold one level of balanced brackets, the url ends at whitespace or the matching ")" (one level of parens). A title is ignored.
  function linkParts(ctx, s, i) {
    var n = s.length, depth = 0, j = i;
    var limit = Math.min(n, i + 4000);
    for (; j < limit; j++) {
      var ch = s.charAt(j);
      if (ch === '\\') { j++; continue; }
      if (ch === '[') { depth++; } else if (ch === ']') { depth--; if (depth === 0) { break; } }
    }
    ctx.pay(j - i + 1);
    if (j >= limit || s.charAt(j) !== ']' || s.charAt(j + 1) !== '(') { return null; }
    var label = s.substring(i + 1, j);
    var k = j + 2, pd = 0, urlEnd = -1, stop = Math.min(n, k + MAX_URL + 400);
    for (; k < stop; k++) {
      var c = s.charAt(k);
      if (c === '(') { pd++; } else if (c === ')') { if (pd === 0) { urlEnd = k; break; } pd--; } else if (c === ' ' || c === '\n' || c === '\t') { break; }
    }
    ctx.pay(k - j);
    var url = s.substring(j + 2, k);
    if (urlEnd === -1) { // whitespace after the url: only a QUOTED title may follow, then the closing paren; anything else is not a link
      var t = k;
      while (t < n && (s.charAt(t) === ' ' || s.charAt(t) === '\t') && t < k + 40) { t++; }
      var q = s.charAt(t);
      if (q !== '"' && q !== "'") { return null; }
      var te = s.indexOf(q, t + 1);
      if (te === -1 || te > t + 400) { return null; }
      var u = te + 1;
      while (u < n && (s.charAt(u) === ' ' || s.charAt(u) === '\t') && u < te + 40) { u++; }
      if (s.charAt(u) !== ')') { return null; }
      ctx.pay(u - k + 1);
      urlEnd = u;
    }
    return { label: label, url: url, end: urlEnd + 1 };
  }

  // ---- blocks ----
  var RE_FENCE = /^ {0,3}(```|~~~)/;
  var RE_HEAD = /^ {0,3}(#{1,6})[ \t]+(.*)$/;
  var RE_HR = /^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$/;
  var RE_QUOTE = /^ {0,3}>[ ]?(.*)$/;
  var RE_ITEM = /^( *)([-*+]|\d{1,9}[.)])[ \t]+(.*)$/;

  function startsBlock(line) {
    return RE_FENCE.test(line) || RE_HEAD.test(line) || RE_HR.test(line) || RE_QUOTE.test(line) || RE_ITEM.test(line);
  }
  function cells(line) {
    var t = line.trim();
    if (t.charAt(0) === '|') { t = t.substring(1); }
    if (t.charAt(t.length - 1) === '|') { t = t.substring(0, t.length - 1); }
    return t.split('|');
  }

  function blocks(ctx, parent, lines, depth) {
    var i = 0, n = lines.length;
    while (i < n) {
      var line = lines[i];
      ctx.pay(line.length + 1);
      if (line.trim() === '') { i++; continue; }
      var m;
      if ((m = RE_FENCE.exec(line))) {
        var fence = m[1], body = [];
        i++;
        while (i < n && lines[i].trim().indexOf(fence) !== 0 && body.length < MAX_FENCE_LINES) { ctx.pay(lines[i].length + 1); body.push(lines[i]); i++; }
        if (i < n && lines[i].trim().indexOf(fence) === 0) { i++; } else if (i < n) { ctx.partial = true; while (i < n && lines[i].trim().indexOf(fence) !== 0) { i++; } i++; }
        var pre = ctx.mk('pre'), code = ctx.mk('code');
        ctx.text(code, body.join('\n'));
        pre.appendChild(code); parent.appendChild(pre);
        continue;
      }
      if ((m = RE_HEAD.exec(line))) {
        var h = ctx.mk('div', 'md-h md-h' + m[1].length);
        h.setAttribute('role', 'heading');
        h.setAttribute('aria-level', String(Math.min(6, 3 + m[1].length))); // never an h1 to h3: a message cannot take over the page's own outline
        inline(ctx, h, stripClosingHashes(m[2]), 0);
        parent.appendChild(h); i++; continue;
      }
      if (RE_HR.test(line)) { parent.appendChild(ctx.mk('hr')); i++; continue; }
      if (RE_QUOTE.test(line)) {
        var q = [];
        while (i < n && (m = RE_QUOTE.exec(lines[i]))) { ctx.pay(lines[i].length + 1); q.push(m[1]); i++; }
        var bq = ctx.mk('blockquote');
        if (depth < MAX_DEPTH) { blocks(ctx, bq, q, depth + 1); } else { ctx.partial = true; ctx.text(bq, q.join(' ')); }
        parent.appendChild(bq); continue;
      }
      if (line.indexOf('|') !== -1 && i + 1 < n && isSepLine(lines[i + 1])) {
        var head = cells(line), table = ctx.mk('table'), thead = ctx.mk('thead'), tbody = ctx.mk('tbody'), tr = ctx.mk('tr');
        for (var c = 0; c < head.length && c < MAX_TABLE_COLS; c++) { var th = ctx.mk('th'); inline(ctx, th, head[c].trim(), 0); tr.appendChild(th); }
        if (head.length > MAX_TABLE_COLS) { ctx.partial = true; }
        thead.appendChild(tr); table.appendChild(thead);
        i += 2;
        var rows = 0;
        while (i < n && lines[i].trim() !== '' && lines[i].indexOf('|') !== -1) {
          ctx.pay(lines[i].length + 1);
          if (rows >= MAX_TABLE_ROWS) { ctx.partial = true; i++; continue; }
          var cs = cells(lines[i]), r = ctx.mk('tr');
          for (var d = 0; d < cs.length && d < MAX_TABLE_COLS; d++) { var td = ctx.mk('td'); inline(ctx, td, cs[d].trim(), 0); r.appendChild(td); }
          if (cs.length > MAX_TABLE_COLS) { ctx.partial = true; }
          tbody.appendChild(r); rows++; i++;
        }
        table.appendChild(tbody); parent.appendChild(table);
        continue;
      }
      if (RE_ITEM.exec(line)) { i = list(ctx, parent, lines, i, 0); continue; }
      // a paragraph: consecutive lines that start no other block
      var para = [];
      while (i < n && lines[i].trim() !== '' && (para.length === 0 || !startsBlock(lines[i]))) { ctx.pay(lines[i].length + 1); para.push(trimRight(lines[i])); i++; }
      var p = ctx.mk('p');
      inline(ctx, p, para.join(' '), 0);
      parent.appendChild(p);
    }
  }

  // list builds one list starting at lines[i] and returns the index after it. Items nest by indentation (at most MAX_LIST_DEPTH deep); a deeper line that is not an item continues the last item as text.
  function list(ctx, parent, lines, i, level) {
    var n = lines.length, first = RE_ITEM.exec(lines[i]), base = first[1].length;
    var ordered = /\d/.test(first[2].charAt(0));
    var ul = ctx.mk(ordered ? 'ol' : 'ul'), last = null;
    while (i < n) {
      var line = lines[i];
      ctx.pay(line.length + 1);
      if (line.trim() === '') { break; }
      var m = RE_ITEM.exec(line);
      if (m && m[1].length < base) { break; }
      if (m && m[1].length > base && last && level < MAX_LIST_DEPTH) { i = list(ctx, last, lines, i, level + 1); continue; }
      if (m && m[1].length >= base) {
        last = ctx.mk('li');
        inline(ctx, last, m[3], 0);
        ul.appendChild(last); i++; continue;
      }
      if (!m && last && (/^ {2,}/.test(line))) { ctx.text(last, ' '); inline(ctx, last, line.trim(), 0); i++; continue; }
      break;
    }
    parent.appendChild(ul);
    return i;
  }

  // ---- entry point ----
  function render(text, opts) {
    var out = { node: null, partial: false };
    var ctx = new Ctx(opts && typeof opts.linkPolicy === 'function' ? opts.linkPolicy : null);
    var root;
    var s = '';
    try { s = clean(text == null ? '' : text); } catch (e) { s = ''; }
    if (s.length > MAX_INPUT) { s = s.substring(0, MAX_INPUT); ctx.partial = true; }
    try {
      root = document.createElement('div');
      root.className = 'md';
      var all = s.split('\n');
      for (var li = 0; li < all.length; li++) { if (all[li].length > MAX_LINE) { all[li] = all[li].substring(0, MAX_LINE); ctx.partial = true; } }
      blocks(ctx, root, all, 0);
    } catch (e) {
      if (e && e.over === true) { ctx.partial = true; } else {
        // an internal fault: the cleaned text in a plain block, never half a document
        root = document.createElement('div');
        root.className = 'md md-plain';
        root.appendChild(document.createTextNode(s));
        ctx.partial = true;
      }
    }
    if (ctx.partial) {
      var note = document.createElement('p');
      note.className = 'md-partial';
      note.appendChild(document.createTextNode('\u2026 (shown in part)'));
      root.appendChild(note);
    }
    out.node = root;
    out.partial = ctx.partial;
    return out;
  }
  window.AccountMarkdown = { render: render, clean: clean };
})();
