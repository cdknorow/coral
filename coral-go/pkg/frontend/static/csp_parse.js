/* Parser for the restricted handler grammar used by csp_compat.js, plus the SHAPE function
 * that reduces a parsed handler to its structure (callee names, statement sequence, argument
 * kinds; literal values removed). No imports: the shape generator (tools/gen_handler_shapes.mjs)
 * and the runtime use the very same functions, so the generated table matches by construction. */

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


/** Literal kind letter: S string, N number, B boolean, Z null/undefined. */
function litKind(v) {
    return typeof v === 'string' ? 'S' : typeof v === 'number' ? 'N' : typeof v === 'boolean' ? 'B' : 'Z';
}

function sigNode(n) {
    switch (n.k) {
        case 'lit': return litKind(n.v);
        case 'id': return `id:${n.name}`;
        case 'arr': return `[${n.items.map(sigNode).join(',')}]`;
        case 'obj': return `{${n.props.map(([k, v]) => `${k}:${sigNode(v)}`).join(',')}}`;
        case 'not': return `!${sigNode(n.e)}`;
        case 'and': case 'or': case 'add': return `(${sigNode(n.l)}${{ and: '&&', or: '||', add: '+' }[n.k]}${sigNode(n.r)})`;
        case 'cmp': return `(${sigNode(n.l)}${n.op}${sigNode(n.r)})`;
        case 'cond': return `(${sigNode(n.c)}?${sigNode(n.a)}:${sigNode(n.b)})`;
        case 'arrow': return `(${n.params.join(',')})=>${sigNode(n.body)}`;
        case 'assign': return `${sigNode(n.target)}=${sigNode(n.e)}`;
        case 'member': return `${sigNode(n.obj)}${n.optional ? '?.' : '.'}${n.prop}`;
        case 'call': return `${sigNode(n.fn)}${n.optional ? '?.' : ''}(${n.args.map(sigNode).join(',')})`;
        default: throw new Error('bad node ' + n.k);
    }
}

function sigStmt(s) {
    switch (s.k) {
        case 'expr': return sigNode(s.e);
        case 'return': return `return ${sigNode(s.e)}`;
        case 'var': return `var ${s.name}=${sigNode(s.e)}`;
        case 'if': return `if(${sigNode(s.cond)}){${s.body.map(sigStmt).join(';')}}`;
        default: throw new Error('bad stmt ' + s.k);
    }
}

/** The shape string of a handler's source text: structure and names, no literal values. */
export function shapeOf(src) {
    return parse(src).map(sigStmt).join(';');
}

/** Number of statements, counting nested if bodies. */
export function statementCount(src) {
    const count = list => list.reduce((n, s) => n + 1 + (s.k === 'if' ? count(s.body) : 0), 0);
    return count(parse(src));
}
