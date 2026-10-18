import { test } from 'node:test';
import assert from 'node:assert/strict';
import { logLevel } from '../assets/lib/logs.js';

test('logLevel goes by what a line says about itself', async (t) => {
  const cases = [
    ['[Client ERROR] Failed to connect', 'err'],
    ['[Client/Socket WARN] Keepalive timed out', 'warn'],
    ['[Client INFO] Connected', ''],
    ['[Client DEBUG] Sending ping', 'debug'],
    ['[dev] → 15551234567@s.whatsapp.net: the error is on line 3', 'dev'],
    ['warning: data_dir /Users/you is your home folder', 'warn'],
    ['WARNING: anyone who can message this WhatsApp account can use the assistant', 'warn'],
    ['panic: runtime error: index out of range', 'err'],
    ['backup work: FAILED: rclone exited with 1', 'err'],
    ['brain work: error for 15551234567@s.whatsapp.net: exit status 1', 'err'],
    ['HTTP send error to 15551234567@s.whatsapp.net: not connected', 'err'],
    ['WhatsApp connect failed: dial tcp: timeout — retrying in 5s', 'err'],
    ['STARTUP CHECK FAILED: /Users/you/creds.json', 'err'],
    // What the line quotes does not count.
    ['brain work: sending response (42 chars) to 15551234567@s.whatsapp.net', ''],
    ['routing message for 15551234567@s.whatsapp.net: brain work: queue full, the error count is 3', ''],
    ['plaud rec1: handed "error log review" to brain work', ''],
    ['Bridge started', ''],
    ['', ''],
  ];
  for (const [line, want] of cases) {
    await t.test(line || 'empty', () => assert.equal(logLevel(line), want));
  }
});
