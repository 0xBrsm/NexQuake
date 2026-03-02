#!/usr/bin/env node

const fs = require('fs');
const path = require('path');

const START_MARKER = '<!-- pages:home-features:start -->';
const END_MARKER = '<!-- pages:home-features:end -->';

function fail(message) {
  console.error(message);
  process.exit(1);
}

const [, , inputPath, outputPath] = process.argv;

if (!inputPath || !outputPath) {
  fail('usage: node src/site/extract-home-features.js <input-readme> <output-json>');
}

const source = fs.readFileSync(inputPath, 'utf8');
const start = source.indexOf(START_MARKER);
const end = source.indexOf(END_MARKER);

if (start === -1 || end === -1 || end <= start) {
  fail('pages:home-features markers not found or out of order');
}

const block = source.slice(start + START_MARKER.length, end).trim();
const lines = block.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
const features = lines.map((line) => {
  const match = line.match(/^-\s+\*\*(.+?)\*\*:\s+(.+)$/);
  if (!match) {
    fail(`invalid home feature line: ${line}`);
  }

  return {
    title: match[1],
    description: match[2],
  };
});

if (!features.length) {
  fail('no home features found between markers');
}

fs.mkdirSync(path.dirname(outputPath), { recursive: true });
fs.writeFileSync(outputPath, JSON.stringify(features, null, 2) + '\n');
