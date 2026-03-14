#!/usr/bin/env node
'use strict';

const childProcess = require('child_process');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const DATA_START = '// <!-- site-data:start -->';
const DATA_END = '// <!-- site-data:end -->';
const CANONICAL_BASE = 'https://quake.nexus';
const GITHUB_SRC_BASE = 'https://github.com/0xBrsm/NexQuake/blob/main/src/';

function fail(message) {
  console.error(`generate-doc-pages: ${message}`);
  process.exit(1);
}

function extractBetween(source, startMarker, endMarker, label) {
  const start = source.indexOf(startMarker);
  const end = source.indexOf(endMarker);
  if (start === -1 || end === -1 || end <= start) {
    fail(`markers not found or out of order for "${label}"`);
  }
  return source.slice(start + startMarker.length, end);
}

function extractMatch(source, pattern, label) {
  const match = source.match(pattern);
  if (!match) {
    fail(`failed to extract ${label}`);
  }
  return match[0];
}

function escapeHtml(value) {
  return String(value)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function sanitizePath(value) {
  return String(value || '')
    .replace(/\\\\/g, '/')
    .replace(/^\.\/+/, '')
    .replace(/\/+/g, '/')
    .replace(/^\/+/, '')
    .trim();
}

function normalizePath(value) {
  return sanitizePath(value).toLowerCase();
}

function resolveSourcePath(basePath, targetPath) {
  const baseParts = sanitizePath(basePath).split('/').filter(Boolean);
  const targetParts = sanitizePath(targetPath).split('/');
  const resolved = targetPath.startsWith('/') ? [] : baseParts.slice(0, Math.max(baseParts.length - 1, 0));

  for (const part of targetParts) {
    if (!part || part === '.') {
      continue;
    }

    if (part === '..') {
      if (resolved.length) {
        resolved.pop();
      }
      continue;
    }

    resolved.push(part);
  }

  return resolved.join('/');
}

function githubSource(pathname) {
  return GITHUB_SRC_BASE + pathname;
}

function pageHref(id) {
  if (id === 'home') {
    return '/';
  }
  return '/' + id + '/';
}

function renderNav(PAGES, NAV_SECTIONS, currentPageId) {
  return NAV_SECTIONS.map((section) => `
    <div class="nav-section">
      ${section.label ? `<div class="nav-section-label">${section.label}</div>` : ''}
      ${section.items.map((id) => {
        const label = id === 'home' ? 'Home' : PAGES[id].navLabel;
        const active = id === currentPageId ? ' active' : '';
        return `<a class="nav-item${active}" href="${pageHref(id)}">${label}</a>`;
      }).join('')}
    </div>
  `).join('');
}

function renderMarkdown(markdownPath, rendererPath) {
  return childProcess.execFileSync(
    'python3',
    [rendererPath],
    {
      encoding: 'utf8',
      input: fs.readFileSync(markdownPath, 'utf8'),
    },
  );
}

function writeSitemap(targetRoot, pageList) {
  const urls = ['/', ...pageList.map((page) => pageHref(page.id))];
  const body = urls.map((href) => `  <url>\n    <loc>${CANONICAL_BASE}${href}</loc>\n  </url>`).join('\n');
  fs.writeFileSync(
    path.join(targetRoot, 'sitemap.xml'),
    `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n${body}\n</urlset>\n`,
    'utf8',
  );
}

function rewriteDocLinks(html, currentDocPath, localPathToId) {
  return html.replace(/<a\b([^>]*?)href=(["'])(.*?)\2([^>]*)>/gi, (match, before, quote, href, after) => {
    if (!href || /^(https?:|mailto:|tel:)/i.test(href) || href.startsWith('#')) {
      if (/^https?:/i.test(href) && !/\btarget=/.test(match)) {
        return `<a${before}href=${quote}${href}${quote}${after} target="_blank" rel="noopener">`;
      }
      return match;
    }

    const hashIndex = href.indexOf('#');
    const pathPart = hashIndex >= 0 ? href.slice(0, hashIndex) : href;
    const anchorPart = hashIndex >= 0 ? href.slice(hashIndex) : '';
    const resolvedPath = resolveSourcePath(currentDocPath, pathPart);
    const localId = localPathToId[normalizePath(resolvedPath)];

    if (localId) {
      return `<a${before}href=${quote}${pageHref(localId)}${anchorPart}${quote}${after}>`;
    }

    if (resolvedPath) {
      return `<a${before}href=${quote}${githubSource(resolvedPath)}${anchorPart}${quote}${after} target="_blank" rel="noopener">`;
    }

    return match;
  });
}

function rewriteAssetLinks(html) {
  return html
    .replace(/href="client\/shell\//g, 'href="/client/shell/')
    .replace(/src="client\/shell\//g, 'src="/client/shell/')
    .replace(/url\('assets\//g, "url('/assets/")
    .replace(/url\("assets\//g, 'url("/assets/');
}

function main() {
  const targetRoot = path.resolve(process.argv[2] || '.');
  const indexPath = path.join(targetRoot, 'index.html');
  const rendererPath = path.join(__dirname, 'render-markdown.py');

  if (!fs.existsSync(indexPath)) {
    fail(`index.html not found in ${targetRoot}`);
  }
  if (!fs.existsSync(rendererPath)) {
    fail(`renderer script not found: ${rendererPath}`);
  }

  const indexHtml = fs.readFileSync(indexPath, 'utf8');
  const dataBlock = extractBetween(indexHtml, DATA_START, DATA_END, 'site-data');
  const dataWrapper = `(function(){${dataBlock}\nreturn { PAGE_LIST, NAV_SECTIONS };})()`;
  const { PAGE_LIST, NAV_SECTIONS } = vm.runInNewContext(dataWrapper, {});
  const PAGES = Object.fromEntries(PAGE_LIST.map((page) => [page.id, page]));
  const localPathToId = {};

  for (const page of PAGE_LIST) {
    localPathToId[normalizePath(page.path)] = page.id;
  }

  const topbarHtml = extractMatch(indexHtml, /<header class="topbar">[\s\S]*?<\/header>/, 'topbar')
    .replace('href="/" onclick="navigate(\'home\');return false;"', 'href="/"')
    .replace('href="#home" onclick="navigate(\'home\');return false;"', 'href="/"');
  const styleBlock = extractMatch(indexHtml, /<style>[\s\S]*?<\/style>/, 'style block');

  for (const page of PAGE_LIST) {
    const markdownPath = path.join(targetRoot, page.path);
    if (!fs.existsSync(markdownPath)) {
      fail(`missing markdown source for ${page.id}: ${markdownPath}`);
    }

    let docHtml = renderMarkdown(markdownPath, rendererPath);
    docHtml = rewriteDocLinks(docHtml, page.path, localPathToId);

    const outputDir = path.join(targetRoot, page.id);
    const outputPath = path.join(outputDir, 'index.html');
    fs.mkdirSync(outputDir, { recursive: true });

    const canonicalUrl = CANONICAL_BASE + pageHref(page.id);
    const pageTitle = `NexQuake Docs | ${page.title}`;
    const pageDescription = page.summary;
    const navHtml = renderNav(PAGES, NAV_SECTIONS, page.id);

    const documentHtml = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <meta name="color-scheme" content="dark">
  <meta name="theme-color" content="#0a0a0a">
  <meta name="application-name" content="NexQuake">
  <meta name="description" content="${escapeHtml(pageDescription)}">
  <meta name="robots" content="index,follow,max-image-preview:large,max-snippet:-1,max-video-preview:-1">
  <meta property="og:type" content="article">
  <meta property="og:site_name" content="NexQuake">
  <meta property="og:title" content="${escapeHtml(pageTitle)}">
  <meta property="og:description" content="${escapeHtml(pageDescription)}">
  <meta property="og:url" content="${canonicalUrl}">
  <meta name="twitter:card" content="summary">
  <meta name="twitter:title" content="${escapeHtml(pageTitle)}">
  <meta name="twitter:description" content="${escapeHtml(pageDescription)}">
  <link rel="canonical" href="${canonicalUrl}">
  <title>${escapeHtml(pageTitle)}</title>
  <link rel="icon" href="/client/shell/favicon.svg" type="image/svg+xml">
${styleBlock}
  <link rel="stylesheet" href="/client/shell/shell-nq.css">
</head>
<body>
<div class="layout">
${topbarHtml}
  <nav class="sidebar" aria-label="Documentation navigation" data-nosnippet>
${navHtml}
  </nav>
  <div class="content-wrap" id="content-wrap">
    <main class="content">
      <div class="md">
${docHtml}
      </div>
    </main>
  </div>
</div>
<script src="/doc-shell.js"></script>
</body>
</html>
`;

    fs.writeFileSync(outputPath, rewriteAssetLinks(documentHtml), 'utf8');
  }

  writeSitemap(targetRoot, PAGE_LIST);
}

main();
