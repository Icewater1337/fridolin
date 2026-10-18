// Reading the bridge's log lines: how serious one is. Pure, so unit-tested
// (web/test/logs.test.js).

const TAGS = { ERROR: 'err', WARN: 'warn', INFO: '', DEBUG: 'debug' };

/**
 * The level of a log line (after Go's timestamp): 'err', 'warn', 'dev',
 * 'debug' or ''. It goes by what the line says about itself, not by what
 * it quotes: whatsmeow's "[Client ERROR]" tag, a leading "warning:" or
 * "panic:", a dev-mode "[dev]" line, or "error" or "failed" before the
 * text the line quotes ("backup work: FAILED: …", "brain work: error for
 * …: …"), leaving out what it puts in quotes. A message that merely
 * contains the word is not an error.
 */
export function logLevel(text) {
  const t = String(text || '');
  const tag = /^\[[^\]\s]+ (ERROR|WARN|INFO|DEBUG)\]/.exec(t);
  if (tag) return TAGS[tag[1]];
  if (t.startsWith('[dev]')) return 'dev';
  if (/^(panic|fatal error):/.test(t)) return 'err';
  if (/^warning:/i.test(t)) return 'warn';
  const head = t.replace(/"[^"]*"/g, '""').split(': ').slice(0, 2).join(': ');
  if (/\b(error|failed)\b/i.test(head)) return 'err';
  return '';
}
