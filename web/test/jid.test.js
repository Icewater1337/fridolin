import { test } from 'node:test';
import assert from 'node:assert/strict';
import { isGroupJid, GROUP_JID_RE, shortJid, phone, phoneish, senderLabel } from '../assets/lib/jid.js';
import { waKind, waLabel } from '../assets/lib/whatsapp.js';

const DM = '15551234567@s.whatsapp.net';
const GROUP = '120363000000000000@g.us';

test('isGroupJid', async (t) => {
  const cases = [[GROUP, true], [DM, false], ['', false], [null, false], [42, false]];
  for (const [jid, want] of cases) {
    await t.test(String(jid), () => assert.equal(isGroupJid(jid), want));
  }
});

test('GROUP_JID_RE takes what the bridge allows', async (t) => {
  // The same cases as TestAllowListEntries in dashboard_test.go.
  const cases = [
    [GROUP, true],
    ['15551234567-1400000000@g.us', true], // personal-data:ignore (an old-style group JID made of placeholders)
    [` ${GROUP} `, false], // the routing editor trims first
    [`${GROUP}/x`, false],
    ['x@g.us', false],
    ['@g.us', false],
    ['-1@g.us', false],
    [DM, false],
    ['', false],
  ];
  for (const [jid, want] of cases) {
    await t.test(jid || 'empty', () => assert.equal(GROUP_JID_RE.test(jid), want));
  }
});

test('shortJid', async (t) => {
  const cases = [
    ['empty', '', ''],
    ['user', DM, '+15551234567'],
    ['long group', GROUP, 'group …000000'],
    ['short group', '12345@g.us', 'group 12345'],
    ['lid', '120363000000000001@lid', 'id …000001'],
    ['bare number', '15551234567', '+15551234567'],
    ['short other', 'status@broadcast', 'status@broadcast'],
    ['long other', 'abcdefghijklmnopqrstuvwxyz@newsletter', 'abcdefghijkl…newsletter'],
  ];
  for (const [name, jid, want] of cases) {
    await t.test(name, () => assert.equal(shortJid(jid), want));
  }
});

test('phone and phoneish', async (t) => {
  await t.test('adds one +', () => assert.equal(phone('15551234567'), '+15551234567'));
  await t.test('keeps an existing +', () => assert.equal(phone('+15551234567'), '+15551234567'));
  await t.test('empty', () => assert.equal(phone(''), ''));
  await t.test('phoneish formats a number', () => assert.equal(phoneish('15551234567'), '+15551234567'));
  await t.test('phoneish keeps a name', () => assert.equal(phoneish('Alex'), 'Alex'));
  await t.test('phoneish keeps short digits', () => assert.equal(phoneish('12345'), '12345'));
});

test('senderLabel', async (t) => {
  const cases = [
    ['nobody', '', DM, 'Someone'],
    ['a number', '15551234567', GROUP, '+15551234567'],
    ['a JID', DM, GROUP, '+15551234567'],
    ['a push name', 'Alex', GROUP, 'Alex'],
    ['the DM itself', 'lid-alias', 'lid-alias', 'lid-alias'],
  ];
  for (const [name, from, chat, want] of cases) {
    await t.test(name, () => assert.equal(senderLabel(from, chat), want));
  }
});

test('WhatsApp mode labels', async (t) => {
  const cases = [
    ['connected', 'ok', 'WhatsApp connected'],
    ['logged-out', 'bad', 'Logged out of WhatsApp'],
    ['disabled', 'neutral', 'WhatsApp off'],
    ['something-new', 'neutral', 'WhatsApp: something-new'],
    ['', 'neutral', 'WhatsApp'],
  ];
  for (const [mode, kind, label] of cases) {
    await t.test(mode || 'empty', () => {
      assert.equal(waKind(mode), kind);
      assert.equal(waLabel(mode), label);
    });
  }
});
