/* Inline event handler support for pages served under a strict CSP.
 *
 * The Coral UI was written with inline handlers (onclick="selectSession('x')").
 * The Cloud Hub serves it with `script-src 'self'` (no 'unsafe-inline'), because
 * an XSS on the hub origin could drive every enrolled machine, so the browser
 * refuses to run those attributes. This module runs them WITHOUT eval or
 * Function: it reads the attribute text, parses it with a small restricted
 * grammar, and executes only:
 *
 *   - calls to functions the app itself defined on window (not native ones such
 *     as fetch/eval/open), with literal, `this`, `event` or `this.<chain>`
 *     arguments;
 *   - `event.stopPropagation()/preventDefault()`, `if (<cond>) { ... }` with
 *     simple boolean conditions on event properties, `return <call>`;
 *   - harmless DOM calls on `this` (closest, remove, select, classList.toggle...).
 *
 * Anything outside the grammar is ignored (and logged once), never evaluated.
 * It is only active when the page's boot data says mode "cloud"; the standalone
 * Coral keeps using native handlers.
 *
 * Events are delegated from `document`, so dynamically inserted markup works
 * and handlers fire innermost-first like the native ones; stopPropagation()
 * halts the walk up the ancestors. */

import { isCloud } from './server_base.js';

const EVENTS = ['click', 'dblclick', 'change', 'input', 'keydown', 'keyup', 'submit', 'mousedown', 'contextmenu'];

// ── tokenizer ───────────────────────────────────────────────────────

export function tokenize(src) {
    const out = [];
    let i = 0;
    while (i < src.length) {
        const c = src[i];
        if (/\s/.test(c)) { i++; continue; }
        if (c === '"' || c === "'") {
            let j = i + 1, s = '';
            while (j < src.length && src[j] !== c) {
                if (src[j] === '\\') {
                    const n = src[j + 1];
                    if (n === undefined) throw new SyntaxError('bad escape');
                    const map = { n: '\n', t: '\t', r: '\r', '0': '\0' };
                    if (n === 'x') { s += String.fromCharCode(parseInt(src.slice(j + 2, j + 4), 16)); j += 4; continue; }
                    if (n === 'u') { s += String.fromCharCode(parseInt(src.slice(j + 2, j + 6), 16)); j += 6; continue; }
                    s += map[n] !== undefined ? map[n] : n; j += 2; continue;
                }
                s += src[j++];
            }
            if (src[j] !== c) throw new SyntaxError('unterminated string');
            out.push({ t: 'str', v: s }); i = j + 1; continue;
        }
        if (/[0-9]/.test(c) || (c === '-' && /[0-9]/.test(src[i + 1] || '') && (!out.length || ['(', ',', '[', 'op'].includes(out[out.length - 1].t) || out[out.length - 1].t === 'p'))) {
            const m = /^-?\d+(\.\d+)?/.exec(src.slice(i));
            out.push({ t: 'num', v: Number(m[0]) }); i += m[0].length; continue;
        }
        if (/[A-Za-z_$]/.test(c)) {
            const m = /^[A-Za-z_$][\w$]*/.exec(src.slice(i));
            out.push({ t: 'id', v: m[0] }); i += m[0].length; continue;
        }
        const three = src.slice(i, i + 3), two = src.slice(i, i + 2);
        if (three === '===' || three === '!==') { out.push({ t: 'op', v: three }); i += 3; continue; }
        if (['&&', '||', '==', '!='].includes(two)) { out.push({ t: 'op', v: two }); i += 2; continue; }
        if (two === '=>' || two === '?.') { out.push({ t: 'p', v: two }); i += 2; continue; }
        if ('().,;[]{}!?:=+'.includes(c)) { out.push({ t: 'p', v: c }); i++; continue; }
        throw new SyntaxError(`unexpected "${c}"`);
    }
    return out;
}

// ── parser (statements and expressions) ─────────────────────────────

export function parse(src) {
    const toks = tokenize(src);
    let p = 0;
    const peek = () => toks[p];
    const isP = v => toks[p] && toks[p].t === 'p' && toks[p].v === v;
    const isOp = v => toks[p] && toks[p].t === 'op' && toks[p].v === v;
    const expectP = v => { if (!isP(v)) throw new SyntaxError(`expected ${v}`); p++; };

    function isArrowAhead() {
        // ( a, b ) =>   or   ident =>
        if (toks[p] && toks[p].t === 'id' && toks[p + 1] && toks[p + 1].t === 'p' && toks[p + 1].v === '=>') return true;
        if (!isP('(')) return false;
        let d = 0, q = p;
        for (; q < toks.length; q++) {
            if (toks[q].t === 'p' && toks[q].v === '(') d++;
            if (toks[q].t === 'p' && toks[q].v === ')') { d--; if (d === 0) break; }
        }
        return toks[q + 1] && toks[q + 1].t === 'p' && toks[q + 1].v === '=>';
    }
    function primary() {
        if (isArrowAhead()) {
            const params = [];
            if (isP('(')) { p++; while (!isP(')')) { const t = toks[p++]; if (!t || t.t !== 'id') throw new SyntaxError('bad param'); params.push(t.v); if (isP(',')) p++; } expectP(')'); }
            else params.push(toks[p++].v);
            expectP('=>');
            return { k: 'arrow', params, body: assign() };
        }
        const t = toks[p];
        if (!t) throw new SyntaxError('unexpected end');
        if (t.t === 'str' || t.t === 'num') { p++; return { k: 'lit', v: t.v }; }
        if (t.t === 'id') {
            p++;
            if (t.v === 'true') return { k: 'lit', v: true };
            if (t.v === 'false') return { k: 'lit', v: false };
            if (t.v === 'null') return { k: 'lit', v: null };
            if (t.v === 'undefined') return { k: 'lit', v: undefined };
            return { k: 'id', name: t.v };
        }
        if (isP('[')) {
            p++; const items = [];
            while (!isP(']')) { items.push(assign()); if (isP(',')) p++; else break; }
            expectP(']');
            return { k: 'arr', items };
        }
        if (isP('{')) { // object literal
            p++; const props = [];
            while (!isP('}')) {
                const key = toks[p++];
                if (!key || (key.t !== 'id' && key.t !== 'str')) throw new SyntaxError('bad object key');
                if (['__proto__', 'constructor', 'prototype'].includes(String(key.v))) throw new SyntaxError('bad object key');
                expectP(':');
                props.push([String(key.v), assign()]);
                if (isP(',')) p++; else break;
            }
            expectP('}');
            return { k: 'obj', props };
        }
        if (isP('(')) { p++; const e = assign(); expectP(')'); return e; }
        throw new SyntaxError('unexpected token');
    }
    function postfix() {
        let e = primary();
        for (;;) {
            if (isP('.')) {
                p++;
                const t = toks[p++];
                if (!t || t.t !== 'id') throw new SyntaxError('expected property');
                e = { k: 'member', obj: e, prop: t.v };
            } else if (isP('?.')) {
                p++;
                if (isP('(')) { // optional call: fn?.(args)
                    p++; const args = [];
                    while (!isP(')')) { args.push(assign()); if (isP(',')) p++; else break; }
                    expectP(')');
                    e = { k: 'call', fn: e, args, optional: true };
                } else {
                    const t = toks[p++];
                    if (!t || t.t !== 'id') throw new SyntaxError('expected property');
                    e = { k: 'member', obj: e, prop: t.v, optional: true };
                }
            } else if (isP('(')) {
                p++; const args = [];
                while (!isP(')')) { args.push(assign()); if (isP(',')) p++; else break; }
                expectP(')');
                e = { k: 'call', fn: e, args };
            } else break;
        }
        return e;
    }
    function unary() { if (isP('!')) { p++; return { k: 'not', e: unary() }; } return postfix(); }
    function additive() {
        let l = unary();
        while (isP('+')) { p++; l = { k: 'add', l, r: unary() }; }
        return l;
    }
    function equality() {
        let l = additive();
        while (isOp('===') || isOp('!==') || isOp('==') || isOp('!=')) { const op = toks[p++].v; l = { k: 'cmp', op, l, r: additive() }; }
        return l;
    }
    function and() { let l = equality(); while (isOp('&&')) { p++; l = { k: 'and', l, r: equality() }; } return l; }
    function or() { let l = and(); while (isOp('||')) { p++; l = { k: 'or', l, r: and() }; } return l; }
    function ternary() {
        const c = or();
        if (isP('?')) { p++; const a = assign(); expectP(':'); const b = assign(); return { k: 'cond', c, a, b }; }
        return c;
    }
    function assign() {
        const l = ternary();
        if (isP('=')) {
            if (l.k !== 'member') throw new SyntaxError('bad assignment target');
            p++;
            return { k: 'assign', target: l, e: assign() };
        }
        return l;
    }
    const expr = assign;
    function stmt() {
        const t = peek();
        if (t && t.t === 'id' && t.v === 'if') {
            p++; expectP('('); const cond = expr(); expectP(')');
            let body;
            if (isP('{')) { p++; body = stmts('}'); expectP('}'); } else body = [stmt()];
            return { k: 'if', cond, body };
        }
        if (t && t.t === 'id' && t.v === 'return') { p++; return { k: 'return', e: expr() }; }
        if (t && t.t === 'id' && (t.v === 'const' || t.v === 'let' || t.v === 'var')) {
            p++; const n = toks[p++];
            if (!n || n.t !== 'id') throw new SyntaxError('bad declaration');
            expectP('=');
            return { k: 'var', name: n.v, e: expr() };
        }
        return { k: 'expr', e: expr() };
    }
    function stmts(end) {
        const out = [];
        while (p < toks.length && !(end && isP(end))) {
            if (isP(';')) { p++; continue; }
            out.push(stmt());
            if (isP(';')) p++;
            else if (p < toks.length && !(end && isP(end))) throw new SyntaxError('expected ;');
        }
        return out;
    }
    const prog = stmts(null);
    if (p < toks.length) throw new SyntaxError('trailing tokens');
    return prog;
}

// ── evaluator ───────────────────────────────────────────────────────

// Property names that must never be read, called or assigned through a handler.
const BLOCKED_PROPS = new Set(['constructor', 'prototype', '__proto__', '__defineGetter__', '__defineSetter__',
    'eval', 'Function', 'innerHTML', 'outerHTML', 'insertAdjacentHTML', 'srcdoc', 'ownerDocument', 'defaultView',
    'contentWindow', 'contentDocument', 'cookie', 'localStorage', 'sessionStorage', 'location', 'open', 'write', 'writeln',
    'document', 'parent', 'top', 'opener', 'self', 'frames', 'globalThis', 'import', 'fetch', 'XMLHttpRequest', 'WebSocket',
    'href', 'src', 'action', 'formAction', 'onclick', 'onerror', 'onload', 'setAttribute', 'append', 'appendChild', 'replaceWith']);

// Plain DOM/Event methods a handler may call on `this`, `event` or values derived from them.
const SAFE_METHODS = new Set(['stopPropagation', 'preventDefault', 'stopImmediatePropagation', 'closest', 'remove', 'select', 'focus', 'blur',
    'click', 'toggle', 'add', 'contains', 'getAttribute', 'matches', 'querySelector', 'querySelectorAll', 'scrollIntoView', 'writeText',
    'reset', 'checkValidity', 'forEach']);

// The only properties a handler may assign, and only on DOM elements or their style.
const ASSIGNABLE = new Set(['value', 'textContent', 'checked', 'disabled', 'hidden', 'display', 'visibility', 'opacity']);

const isNative = f => typeof f === 'function' && /\{\s*\[native code\]\s*\}\s*$/.test(Function.prototype.toString.call(f));

function appFunction(name) {
    if (BLOCKED_PROPS.has(name)) return null;
    const f = globalThis[name];
    return typeof f === 'function' && !isNative(f) ? f : null;
}

const ARROW = Symbol('csp_compat.arrow');
const WINDOW = { __window: true };

class Ctx {
    constructor(el, event) {
        this.el = el;
        this.event = event;
        this.stopped = false;
        this.returned = undefined;
        this.vars = new Map();
    }
}

function isElementLike(o) {
    if (o === null || typeof o !== 'object' || o === WINDOW) return false;
    if (typeof Element !== 'undefined') return o instanceof Element || o instanceof CSSStyleDeclaration;
    return true; // node tests use plain objects
}

function documentFacade() {
    return {
        getElementById: id => document.getElementById(id),
        querySelectorAll: sel => document.querySelectorAll(sel),
    };
}

function makeArrow(n, ctx) {
    const f = (...args) => {
        const inner = Object.create(ctx);
        inner.vars = new Map(ctx.vars);
        n.params.forEach((name, i) => inner.vars.set(name, args[i]));
        try { return evalExpr(n.body, inner); } catch (e) { console.error('csp_compat: handler failed:', e.message); return undefined; }
    };
    f[ARROW] = true;
    return f;
}

function evalExpr(n, ctx) {
    switch (n.k) {
        case 'lit': return n.v;
        case 'arr': return n.items.map(i => evalExpr(i, ctx));
        case 'obj': { const o = {}; for (const [k, v] of n.props) o[k] = evalExpr(v, ctx); return o; }
        case 'not': return !evalExpr(n.e, ctx);
        case 'and': return evalExpr(n.l, ctx) && evalExpr(n.r, ctx);
        case 'or': return evalExpr(n.l, ctx) || evalExpr(n.r, ctx);
        case 'cond': return evalExpr(n.c, ctx) ? evalExpr(n.a, ctx) : evalExpr(n.b, ctx);
        case 'add': return evalExpr(n.l, ctx) + evalExpr(n.r, ctx);
        case 'cmp': {
            const a = evalExpr(n.l, ctx), b = evalExpr(n.r, ctx);
            return n.op === '===' ? a === b : n.op === '!==' ? a !== b : n.op === '==' ? a == b : a != b; // eslint-disable-line eqeqeq
        }
        case 'arrow': return makeArrow(n, ctx);
        case 'assign': {
            const t = n.target;
            if (BLOCKED_PROPS.has(t.prop) || !ASSIGNABLE.has(t.prop)) throw new Error(`assignment to ${t.prop} not allowed`);
            const obj = evalExpr(t.obj, ctx);
            if (!isElementLike(obj)) throw new Error('assignment target is not an element');
            const v = evalExpr(n.e, ctx);
            obj[t.prop] = v;
            return v;
        }
        case 'id':
            if (ctx.vars.has(n.name)) return ctx.vars.get(n.name);
            if (n.name === 'this') return ctx.el;
            if (n.name === 'event') return ctx.eventProxy;
            if (n.name === 'navigator') return { clipboard: navigator.clipboard };
            if (n.name === 'window') return WINDOW;
            if (n.name === 'document') return documentFacade();
            throw new Error(`unknown identifier ${n.name}`);
        case 'member': {
            if (BLOCKED_PROPS.has(n.prop)) throw new Error(`blocked property ${n.prop}`);
            const o = evalExpr(n.obj, ctx);
            if (o === WINDOW) { const f = appFunction(n.prop); if (!f) throw new Error(`not an app function: ${n.prop}`); return f; }
            if (o == null) { if (n.optional) return undefined; throw new Error('member of null'); }
            return o[n.prop];
        }
        case 'call': return evalCall(n, ctx);
        default: throw new Error('bad node');
    }
}

function evalCall(n, ctx) {
    const fn = n.fn;
    // setTimeout is allowed only with one of our own arrow functions.
    if (fn.k === 'id' && fn.name === 'setTimeout') {
        const cb = evalExpr(n.args[0], ctx);
        if (!cb || !cb[ARROW]) throw new Error('setTimeout needs an arrow function');
        return setTimeout(cb, Number(evalExpr(n.args[1] || { k: 'lit', v: 0 }, ctx)) || 0);
    }
    const args = n.args.map(a => evalExpr(a, ctx));
    if (fn.k === 'id' && !ctx.vars.has(fn.name)) {
        const f = appFunction(fn.name);
        if (!f) { if (n.optional) return undefined; throw new Error(`not an app function: ${fn.name}`); }
        return f(...args);
    }
    if (fn.k === 'member') {
        const objNode = fn.obj;
        if (objNode.k === 'id' && objNode.name === 'window' && !ctx.vars.has('window')) {
            const f = appFunction(fn.prop);
            if (!f) { if (n.optional || fn.optional) return undefined; throw new Error(`not an app function: ${fn.prop}`); }
            return f(...args);
        }
        if (!SAFE_METHODS.has(fn.prop) && !(objNode.k === 'id' && objNode.name === 'document')) throw new Error(`method not allowed: ${fn.prop}`);
        const obj = evalExpr(objNode, ctx);
        if (obj == null || typeof obj[fn.prop] !== 'function') { if (n.optional) return undefined; throw new Error(`not callable: ${fn.prop}`); }
        if (fn.prop === 'forEach' && !(args[0] && args[0][ARROW])) throw new Error('forEach needs an arrow function');
        return obj[fn.prop](...args);
    }
    // Calling a value (e.g. an arrow bound to a local): only our own arrows.
    const f = evalExpr(fn, ctx);
    if (f && f[ARROW]) return f(...args);
    throw new Error('bad call target');
}

function execStmts(list, ctx) {
    for (const s of list) {
        if (s.k === 'expr') evalExpr(s.e, ctx);
        else if (s.k === 'var') ctx.vars.set(s.name, evalExpr(s.e, ctx));
        else if (s.k === 'return') { ctx.returned = evalExpr(s.e, ctx); return true; }
        else if (s.k === 'if') { if (evalExpr(s.cond, ctx) && execStmts(s.body, ctx)) return true; }
    }
    return false;
}

const cache = new Map();
const warned = new Set();

function program(src) {
    let p = cache.get(src);
    if (p === undefined) {
        try { p = parse(src); } catch (e) { p = null; if (!warned.has(src)) { warned.add(src); console.warn('csp_compat: ignored unsupported handler:', src.slice(0, 80), e.message); } }
        if (cache.size > 2000) cache.clear();
        cache.set(src, p);
    }
    return p;
}

/** Run one handler attribute value for `el`/`event`. Returns false if the handler asked to cancel. */
export function runHandler(src, el, event) {
    const prog = program(src);
    if (!prog) return true;
    const ctx = new Ctx(el, event);
    ctx.eventProxy = new Proxy(event, {
        get(t, k) {
            if (BLOCKED_PROPS.has(k)) return undefined;
            if (k === 'stopPropagation') return () => { ctx.stopped = true; t.stopPropagation(); };
            const v = t[k];
            return typeof v === 'function' ? v.bind(t) : v;
        },
    });
    try { execStmts(prog, ctx); } catch (e) { console.error('csp_compat: handler failed:', e.message); }
    return ctx.returned !== false;
}

function onEvent(ev) {
    const attr = 'on' + ev.type;
    let el = ev.target instanceof Element ? ev.target : null;
    while (el) {
        const src = el.getAttribute && el.getAttribute(attr);
        if (src) {
            const ctx = { stopped: false };
            const ok = runHandlerWithStop(src, el, ev, ctx);
            if (ok === false) ev.preventDefault();
            if (ctx.stopped) return;
        }
        el = el.parentElement;
    }
}

function runHandlerWithStop(src, el, ev, out) {
    const prog = program(src);
    if (!prog) return true;
    const ctx = new Ctx(el, ev);
    ctx.eventProxy = new Proxy(ev, {
        get(t, k) {
            if (BLOCKED_PROPS.has(k)) return undefined;
            if (k === 'stopPropagation') return () => { ctx.stopped = true; };
            const v = t[k];
            return typeof v === 'function' ? v.bind(t) : v;
        },
    });
    try { execStmts(prog, ctx); } catch (e) { console.error('csp_compat: handler failed:', e.message); }
    out.stopped = ctx.stopped;
    return ctx.returned !== false;
}

export function installCspCompat() {
    for (const t of EVENTS) document.addEventListener(t, onEvent);
}

if (typeof document !== 'undefined' && isCloud()) installCspCompat();
