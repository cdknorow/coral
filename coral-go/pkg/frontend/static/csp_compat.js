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
import { parse, shapeOf, statementCount } from './csp_parse.js';
import { SHAPES, EVENTS } from './csp_shapes.js';

export { parse, shapeOf };


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

const MAX_STATEMENTS = 8;
const MAX_SOURCE = 600;
let ACTIVE_SHAPES = SHAPES;

/** Test hook: replace the shape table (never called by app code). */
export function _useShapesForTests(set) { ACTIVE_SHAPES = set || SHAPES; cache.clear(); }

function program(src) {
    let p = cache.get(src);
    if (p === undefined) {
        try {
            if (src.length > MAX_SOURCE) throw new Error('handler too long');
            p = parse(src);
            if (statementCount(src) > MAX_STATEMENTS) throw new Error('too many statements');
            // Allowlist: only handlers shaped like one the shipped UI actually contains run at all.
            // Attacker text can then only ever be an argument VALUE of a known handler.
            if (!ACTIVE_SHAPES.has(shapeOf(src))) throw new Error('handler shape not in the allowlist');
        } catch (e) { p = null; if (!warned.has(src)) { warned.add(src); console.warn('csp_compat: ignored unsupported handler:', src.slice(0, 80), e.message); } }
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
    if (!ev.isTrusted) return; // only real user input; script-dispatched events never run handlers
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
