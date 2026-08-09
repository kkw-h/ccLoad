const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');

const html = fs.readFileSync(path.join(__dirname, '..', '..', 'settings.html'), 'utf8');
const js = fs.readFileSync(path.join(__dirname, 'settings.js'), 'utf8');

test('settings page exposes an accessible external auth environment editor', () => {
  assert.match(html, /id="external-auth-environments"/);
  assert.match(html, /id="external-auth-environment-rows"/);
  assert.match(html, /id="add-external-auth-environment"/);
  assert.match(html, /aria-label="[^"]+"/);
});

test('environment editor uses dedicated CRUD API and validates environment fields', () => {
  assert.match(js, /\/admin\/external-auth-environments/);
  assert.match(js, /pattern="\[a-z0-9_-\]\+"/);
  assert.match(js, /type="url"/);
  assert.match(js, /method:\s*environment\.id\s*\?\s*'PUT'\s*:\s*'POST'/);
  assert.match(js, /method:\s*'DELETE'/);
});
