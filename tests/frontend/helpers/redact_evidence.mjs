// What run_cdp_suites.mjs removes from the evidence it captures at a suite's timeout (a screenshot aside, which shows only the test dashboard) before CI uploads it as a
// run artifact that anyone can read for 7 days: sign-in tokens, keys and passwords in an address or a log line, the same as a JSON member or an Authorization / session-token
// header, and this machine's user and computer names (four characters or more: a shorter one would mangle every word that holds it).
export function redactEvidence(text, names = []) {
  let out = String(text)
    .replace(/(login_token|token|api_key|apikey|key|password|secret)=[^&\s"'<>]+/gi, '$1=REDACTED')
    .replace(/("(?:login_token|token|api_key|apikey|key|password|secret|access_token|session_token)"\s*:\s*)"[^"]*"/gi, '$1"REDACTED"')
    .replace(/\b(authorization\s*[:=]\s*)(?:bearer\s+|basic\s+)?[^\s"',;]+/gi, '$1REDACTED')
    .replace(/\bbearer\s+[A-Za-z0-9._~+/=-]+/gi, 'Bearer REDACTED')
    .replace(/(x-coral-[a-z-]*token\s*[:=]\s*)[^\s"',;]+/gi, '$1REDACTED');
  for (const name of names.filter(n => n && n.length >= 4)) out = out.split(name).join('<redacted>');
  return out;
}
