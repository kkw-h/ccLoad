const assert = require('node:assert/strict');
const test = require('node:test');

const {
  REASONING_EFFORT_ORDER,
  normalizeOverrides,
  upsertOverride,
  deleteOverride
} = require('./model-reasoning-efforts.js');

test('normalizes model reasoning effort overrides in canonical order', () => {
  assert.deepEqual(normalizeOverrides({
    ' GPT-5.6-SOL ': ['HIGH', 'low', 'high']
  }), {
    'gpt-5.6-sol': ['low', 'high']
  });
  assert.deepEqual(REASONING_EFFORT_ORDER, [
    'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'
  ]);
});

test('preserves explicit empty effort arrays', () => {
  assert.deepEqual(normalizeOverrides({ 'no-reasoning': [] }), {
    'no-reasoning': []
  });
});

test('rejects invalid override shapes and values', () => {
  assert.throws(() => normalizeOverrides([]), /object/);
  assert.throws(() => normalizeOverrides({ '   ': ['low'] }), /model name/);
  assert.throws(() => normalizeOverrides({ ['x'.repeat(256)]: ['low'] }), /model name/);
  assert.throws(() => normalizeOverrides({ model: 'low' }), /array/);
  assert.throws(() => normalizeOverrides({ model: ['turbo'] }), /unknown effort/);
});

test('rejects model names that collide after normalization', () => {
  assert.throws(() => normalizeOverrides({
    Model: ['low'],
    ' model ': ['high']
  }), /duplicate model/);
});

test('adds and updates overrides without mutating input', () => {
  const initial = { beta: ['medium'] };
  const added = upsertOverride(initial, ' Alpha ', ['HIGH', 'low']);
  const updated = upsertOverride(added, 'BETA', []);

  assert.deepEqual(initial, { beta: ['medium'] });
  assert.deepEqual(added, { alpha: ['low', 'high'], beta: ['medium'] });
  assert.deepEqual(updated, { alpha: ['low', 'high'], beta: [] });
});

test('deletes overrides and keeps model names sorted', () => {
  const value = normalizeOverrides({ zebra: ['max'], alpha: ['none'], middle: ['medium'] });
  assert.deepEqual(Object.keys(value), ['alpha', 'middle', 'zebra']);
  assert.deepEqual(deleteOverride(value, ' MIDDLE '), {
    alpha: ['none'],
    zebra: ['max']
  });
});
