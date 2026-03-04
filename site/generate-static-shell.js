#!/usr/bin/env node
'use strict';

/**
 * generate-static-shell.js
 *
 * Reads the data declarations (PAGE_LIST, NAV_SECTIONS, HOME_INDEX_SECTIONS,
 * REPO_LAYOUT) from index.html, renders the same static HTML that the browser
 * JS produces, and writes it back between the static-nav and static-main markers.
 *
 * Run after changing any of the data declarations:
 *   node src/site/generate-static-shell.js
 *
 * Pass --check to verify without writing (exits 1 if out of date):
 *   node src/site/generate-static-shell.js --check
 */

const fs = require('fs');
const path = require('path');
const vm = require('vm');

const HTML_FILE = path.join(__dirname, 'index.html');

const DATA_START = '// <!-- site-data:start -->';
const DATA_END = '// <!-- site-data:end -->';
const NAV_START = '<!-- static-nav:start -->';
const NAV_END = '<!-- static-nav:end -->';
const MAIN_START = '<!-- static-main:start -->';
const MAIN_END = '<!-- static-main:end -->';

function fail(msg) {
  console.error(`generate-static-shell: ${msg}`);
  process.exit(1);
}

function extractBetween(src, startMarker, endMarker, label) {
  const s = src.indexOf(startMarker);
  const e = src.indexOf(endMarker);
  if (s === -1 || e === -1 || e <= s) {
    fail(`markers not found or out of order for "${label}"`);
  }
  return { block: src.slice(s + startMarker.length, e), start: s, end: e + endMarker.length };
}

const source = fs.readFileSync(HTML_FILE, 'utf8');

// Extract and evaluate the data declarations.
// Wrap in a function so `const` declarations are accessible as return values.
const { block: dataBlock } = extractBetween(source, DATA_START, DATA_END, 'site-data');
const wrapper = `(function() {
${dataBlock}
return { PAGE_LIST, NAV_SECTIONS, HOME_INDEX_SECTIONS, REPO_LAYOUT };
})()`;
const { PAGE_LIST, NAV_SECTIONS, HOME_INDEX_SECTIONS, REPO_LAYOUT } = vm.runInNewContext(wrapper, {});

if (!PAGE_LIST || !NAV_SECTIONS || !HOME_INDEX_SECTIONS || !REPO_LAYOUT) {
  fail('data extraction failed: one or more required variables are missing');
}

const PAGES = Object.fromEntries(PAGE_LIST.map((p) => [p.id, p]));

function buildRoute(page, anchor) {
  return '#' + page + (anchor ? ('::' + anchor) : '');
}

// --- Nav renderer ---

function renderNavItem(id) {
  if (id === 'home') {
    return `<a class="nav-item" data-page="home" href="#">Home</a>`;
  }
  const doc = PAGES[id];
  return `<a class="nav-item" data-page="${id}" href="${doc.path}">${doc.navLabel}</a>`;
}

function renderNav() {
  return NAV_SECTIONS.map((section) => `
    <div class="nav-section">
      ${section.label ? `<div class="nav-section-label">${section.label}</div>` : ''}
      ${section.items.map(renderNavItem).join('')}
    </div>
  `).join('');
}

// --- Main renderer ---

function renderDocCard(id) {
  const doc = PAGES[id];
  return `
    <a class="doc-card" href="${doc.path}" data-page="${id}">
      <span class="doc-type">${doc.kind}</span>
      <div class="doc-name">${doc.title}</div>
      <div class="doc-summary">${doc.summary}</div>
      <div class="doc-cta">Open Document</div>
    </a>
  `;
}

function renderMapSection(section) {
  const gridClass = 'doc-grid' + (section.items.length === 4 ? ' doc-grid--4' : '');
  return `
    <section class="hub-section">
      <div class="section-head">
        <span class="section-kicker">${section.kicker}</span>
        <h2 class="section-title">${section.title}</h2>
        <p class="section-desc">${section.description}</p>
      </div>
      <div class="${gridClass}">
        ${section.items.map(renderDocCard).join('')}
      </div>
    </section>
  `;
}

function renderLayoutCard(item) {
  const doc = PAGES[item.page];
  return `
    <a class="layout-card" href="${doc.path}" data-page="${item.page}">
      <div class="layout-path">${item.path}</div>
      <div class="layout-summary">${item.summary}</div>
    </a>
  `;
}

function renderMain() {
  const docSections = HOME_INDEX_SECTIONS.map(renderMapSection).join('');
  const layoutSection = `
    <section class="hub-section">
      <div class="section-head">
        <span class="section-kicker">Layout</span>
        <h2 class="section-title">Source Tree</h2>
        <p class="section-desc">Each source directory, linked to its component documentation.</p>
      </div>
      <div class="layout-grid">
        ${REPO_LAYOUT.map(renderLayoutCard).join('')}
      </div>
    </section>
  `;

  return `
      <div class="hero-banner">
        <div class="hero-logo"><span class="nex">Nex</span><span class="quake">Quake</span></div>
        <p class="hero-tagline">
          Click. Frag.
        </p>
        <div class="hero-cmd">
          <span class="comment"># launch NexQuake, then browse to http://your-server:1337</span><br>
          docker run -p 1337:1337 -e CL_ARGS=+connect ghcr.io/0xbrsm/nexquake
        </div>
        <div class="hero-buttons">
          <a class="btn btn-primary" href="${buildRoute('quickstart')}" data-page="quickstart">QUICKSTART</a>
        </div>
      </div>

      ${docSections}
      ${layoutSection}`;
}

// --- Inject back into HTML ---

function replaceBetween(src, startMarker, endMarker, label, newContent) {
  const s = src.indexOf(startMarker);
  const e = src.indexOf(endMarker);
  if (s === -1 || e === -1 || e <= s) {
    fail(`markers not found or out of order when replacing "${label}"`);
  }
  return src.slice(0, s + startMarker.length) + '\n' + newContent + '\n    ' + src.slice(e);
}

const checkMode = process.argv.includes('--check');

let result = source;
result = replaceBetween(result, NAV_START, NAV_END, 'static-nav', renderNav());
result = replaceBetween(result, MAIN_START, MAIN_END, 'static-main', renderMain());

if (checkMode) {
  if (result !== source) {
    console.error('generate-static-shell: static HTML is out of date.');
    console.error('Run: node src/site/generate-static-shell.js');
    process.exit(1);
  }
  console.log('generate-static-shell: static HTML is up to date.');
} else {
  fs.writeFileSync(HTML_FILE, result, 'utf8');
  console.log('generate-static-shell: index.html updated.');
}
