#!/usr/bin/env python3
"""Generate coral-go/pkg/tunnel/routes_query.go: the query parameters each Coral route reads.

The Cloud Hub connector accepts, for a route, only the query parameters its handler
actually reads (anything else is refused). The list is derived by scanning the handlers
in coral-go/internal/server/routes that server.go registers, following helper calls that
receive the request. Parameters that name a file or directory are marked FILE and get the
strict path normalisation in pkg/tunnel/routes.go.

Run from the repository root:  python3 tools/gen_route_queries.py
Then review the diff: it is a security-relevant allowlist.
"""
import glob, re, subprocess, sys

ROOT = 'coral-go/internal/server'
OUT = 'coral-go/pkg/tunnel/routes_query.go'
# Parameter names that carry a file or directory reference.
FILE_PARAMS = {'filepath', 'dir', 'path', 'working_directory', 'file', 'ref', 'cwd', 'folder', 'directory', 'root'}

# Parameters the shipped UI sends that a handler does not (yet) read, found by comparing the real UI's requests
# against this table (coral-hub internal/e2e TestChromeUIPass records them). Add here, with the reason, never
# loosen the table any other way.
EXTRA = {
    # The recorded hub UI sends session_id with the board task list (pkg/tunnel TestRealUIQueriesAreNotRefused); the list
    # is per board, so BoardHandler.ListTasks ignores it. It was allowed before only because a same-named handler read it.
    'GET /api/board/{}/tasks': {'session_id'},
    # The recorded hub UI sends limit with an agent's personal task list; SessionsHandler.ListTasks returns them all.
    'GET /api/sessions/live/{}/tasks': {'limit'},
    # file_explorer.js pages a directory listing with offset; the handler returns the first page regardless.
    'GET /api/sessions/live/{}/search-files': {'offset'},
    # The Agent UI routes are registered by SessionsHandler.RegisterAgentUI, which the registration scan above does not follow.
    # What the handlers read (internal/server/routes/agent_ui.go, agent_ui_request.go): the session, the pinned revision, the event cursor.
    'GET /api/agent/ui': {'session_id'},
    'GET /api/agent/ui/{}/content': {'session_id', 'revision'},
    'GET /api/agent/ui/{}/events': {'session_id', 'after'},
    'POST /api/agent/ui/{}/events': {'session_id'},
    'POST /api/agent/ui-request': {'session_id'},
    # The artifact route is registered by SessionsHandler.RegisterAgentArtifacts (not followed by the scan above). Since v1.4.12
    # (#3168) every artifact link is /api/artifacts/<digest>?v=2; the handler reads v and accepts only absent or 2 (#3179).
    'GET /api/artifacts/{}': {'v'},
}

# Parameters that accept only exact values. The spec token becomes name=a|b. The hub's machine feed connects with feed=hub
# (internal/background and coral-hub internal/feed): a dashboard-less client that must not consume the machine's pending UI
# notifications. Only that exact value is accepted through the tunnel; anything else (feed=, feed=Hub, feed=hub2, two values) is refused.
ENUM = {
    'GET /ws/coral': {'feed': ['hub']},
    # The artifact link form (#3168): only v=2 passes; any other value or a second v is refused.
    'GET /api/artifacts/{}': {'v': ['2']},
}

src = {f: open(f).read() for f in glob.glob(ROOT + '/routes/*.go') if not f.endswith('_test.go')}
B = {}
for f, s in src.items():
    for p in re.split(r'\n(?=func )', s):
        m = re.match(r'func (?:\((\w+) \*?(\w+)\) )?(\w+)\(', p)
        if m:
            B[(m.group(2) or '', m.group(3))] = p

qre = re.compile(r'(?:Query\(\)\.Get|Query\(\)\.Has|\bq\.Get|\bq\.Has|\bquery\.Get|FormValue|PostFormValue)\("([A-Za-z0-9_\-]+)"\)')
# Helper calls that take the request and a literal parameter name, e.g. queryInt(r, "limit", 50).
qre3 = re.compile(r'\b(\w+)\(\s*r\s*,\s*"([A-Za-z0-9_\-]+)"')
# chi.URLParam(r, "name") reads a PATH parameter, not a query parameter.
NOT_QUERY = {'URLParam'}
qre2 = re.compile(r'Query\(\)\["([A-Za-z0-9_\-]+)"\]|\bq\["([A-Za-z0-9_\-]+)"\]')


def params(body, seen, depth=0):
    ps = set(qre.findall(body))
    for a, b in qre2.findall(body):
        ps.add(a or b)
    ps |= {lit for fn, lit in qre3.findall(body) if fn not in NOT_QUERY}
    if depth < 3:
        # Calls in the body only: the function's own signature "Name(w http.ResponseWriter, r *http.Request)" is not a
        # call, and matching it pulled in every handler of the same name (#3279).
        for m in re.finditer(r'(?:\bh\.)?(\w+)\([^()]*\br\b[^()]*\)', body[body.find('{') + 1:]):
            for (rt, fn), b2 in B.items():
                if fn == m.group(1) and (rt, fn) not in seen:
                    seen.add((rt, fn))
                    ps |= params(b2, seen, depth + 1)
    return ps


reg = re.compile(r'r\.(Get|Post|Put|Patch|Delete)\("([^"]+)",\s*([\w\.]+)\)')
sv = open(ROOT + '/server.go').read()
# The handler type behind each variable a route is registered on (boardHandler := routes.NewBoardHandler(...)),
# so a method is matched on its own type: BoardHandler.ListTasks and SessionsHandler.ListTasks used to pool their
# parameters (#3279).
htype = {v: t for v, t in re.findall(r'(?:\bs\.)?(\w+)\s*:?=\s*routes\.New(\w+)\(', sv)}
rows = {}
for m in reg.finditer(sv):
    meth, route, h = m.groups()
    name = h.split('.')[-1]
    want = htype.get(h.split('.')[-2]) if '.' in h else None
    ps = set()
    for (rt, fn), b in B.items():
        if fn == name and rt and (want is None or rt == want):
            ps |= params(b, set())
    # Names that are PATH parameters of this very route are not query parameters (handlers read them through
    # helpers such as chi.URLParam or local wrappers that also take (r, "name")).
    ps -= set(re.findall(r'\{([^}]+)\}', route))
    shape = meth.upper() + ' ' + re.sub(r'\{[^}]*\}', '{}', route)
    rows.setdefault(shape, set()).update(ps)

for shape, extra in EXTRA.items():
    rows.setdefault(shape, set()).update(extra)

lines = []
for shape in sorted(rows):
    if not rows[shape]:
        continue
    enum = ENUM.get(shape, {})
    assert set(enum) <= rows[shape], (shape, enum)
    spec = ','.join(sorted(p + ('*' if p in FILE_PARAMS else '') + ('=' + '|'.join(enum[p]) if p in enum else '') for p in rows[shape]))
    lines.append('\t"%s": "%s",' % (shape, spec))

open(OUT, 'w').write('''// Code generated by tools/gen_route_queries.py; DO NOT EDIT BY HAND. Review the diff of a regeneration.

package tunnel

// routeQueries lists, per route shape ("METHOD /path/{}/more"), the query parameters the
// Coral handler reads. A name ending in "*" is a file or directory reference. A route not
// listed here accepts no query parameters at all. "name=a|b" accepts only those exact values, once.
var routeQueries = map[string]string{
%s
}
''' % '\n'.join(lines))
subprocess.run(['gofmt', '-w', OUT], check=True)
print('wrote', OUT, len(lines), 'routes with query parameters')
