import { test } from 'node:test';
import assert from 'node:assert/strict';
import { IDLE_STOP_DEFAULT, INSTANCE_IDENTITIES, INSTANCE_LIMITS, INSTANCE_SIZES, egressProblem, hostMemoryText, idleStopText, instanceNameProblem, memText, repoProblem, stateKind } from '../assets/lib/fride.js';

test('an instance is named as the bridge allows it', () => {
  assert.equal(instanceNameProblem('work'), '');
  assert.equal(instanceNameProblem('a-1'), '');
  assert.equal(instanceNameProblem('a'.repeat(24)), '');
  assert.match(instanceNameProblem('a'.repeat(25)), /up to 23/);
  assert.match(instanceNameProblem(''), /Give it a name/);
  for (const bad of ['Work', '1work', '-work', 'work_2', 'wörk', 'work ']) assert.match(instanceNameProblem(bad), /lower-case letter/, bad);
  assert.match(instanceNameProblem('native'), /node’s own FrIDE/);
  assert.match(instanceNameProblem('work', ['fridolin', 'work']), /Another instance/);
});

test('repositories and egress hosts are checked as the bridge checks them', () => {
  assert.equal(repoProblem('https://gitlab.example.com/group/app.git'), '');
  assert.match(repoProblem('https://gitlab.example.com/group/my app.git'), /no git URL/);
  assert.equal(egressProblem('registry.example.com'), '');
  for (const bad of ['https://registry.example.com', 'registry.example.com:5000', 'registry.example.com/v2', 'a b']) assert.match(egressProblem(bad), /no host name/, bad);
});

test('memory and idle time read as people say them', () => {
  assert.equal(memText(6144), '6 GB');
  assert.equal(memText(1536), '1.5 GB');
  assert.equal(memText(12288), '12 GB');
  assert.equal(memText(40000), '39 GB');
  assert.equal(memText(512), '512 MB');
  assert.equal(memText(0), '');
  assert.equal(memText(undefined), '');
  assert.equal(hostMemoryText({ version: '1', ready: true, total_memory_mb: 65536, available_memory_mb: 40960, reserve_mb: 6144 }), '64 GB, 40 GB available, 6 GB kept for the machine: 34 GB for instances');
  assert.equal(hostMemoryText({ version: '1', ready: true, total_memory_mb: 16384, available_memory_mb: 4096, reserve_mb: 6144 }), '16 GB, 4 GB available, 6 GB kept for the machine: nothing for instances');
  assert.equal(idleStopText(30), 'stops after 30 minutes idle');
  assert.equal(idleStopText(0), 'never stops by itself', 'in GET /api/fride, 0 is never');
  assert.equal(idleStopText(undefined), 'never stops by itself');
});

test('the states, sizes and bounds are the bridge’s', () => {
  assert.deepEqual(['running', 'starting', 'stopped', 'failed', ''].map(stateKind), ['ok', 'info', 'neutral', 'bad', 'neutral']);
  assert.deepEqual(INSTANCE_SIZES, [['small', 3072], ['medium', 6144], ['large', 12288]]);
  assert.deepEqual(INSTANCE_IDENTITIES, ['github', 'gitlab']);
  assert.equal(IDLE_STOP_DEFAULT, 30);
  assert.deepEqual({ ...INSTANCE_LIMITS }, { instances: 16, list: 64, minMemoryMB: 2048, maxMemoryMB: 65536, maxIdleMinutes: 1440 });
});
