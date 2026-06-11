(() => {
  function isModifierClick(event) {
    return event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0;
  }

  function isSameOriginDocUrl(url) {
    return url.origin === window.location.origin && !url.search && url.pathname.endsWith('/');
  }

  function findAnchor(target) {
    return target && target.closest ? target.closest('a[href]') : null;
  }

  function updateMeta(selector, nextDocument, attribute = 'content') {
    const current = document.querySelector(selector);
    const next = nextDocument.querySelector(selector);
    if (!current || !next) {
      return;
    }
    current.setAttribute(attribute, next.getAttribute(attribute) || '');
  }

  function syncDocumentState(nextDocument) {
    const nextSidebar = nextDocument.querySelector('.sidebar');
    const nextContentWrap = nextDocument.querySelector('.content-wrap');
    if (!nextSidebar || !nextContentWrap) {
      throw new Error('missing page shell');
    }

    const currentSidebar = document.querySelector('.sidebar');
    const currentContentWrap = document.querySelector('.content-wrap');
    if (!currentSidebar || !currentContentWrap) {
      throw new Error('missing current shell');
    }

    currentSidebar.replaceWith(nextSidebar);
    currentContentWrap.replaceWith(nextContentWrap);
    document.title = nextDocument.title;
    updateMeta('meta[name="description"]', nextDocument);
    updateMeta('meta[property="og:title"]', nextDocument);
    updateMeta('meta[property="og:description"]', nextDocument);
    updateMeta('meta[property="og:url"]', nextDocument);
    updateMeta('meta[name="twitter:title"]', nextDocument);
    updateMeta('meta[name="twitter:description"]', nextDocument);
    updateMeta('link[rel="canonical"]', nextDocument, 'href');
  }

  function scrollToHash(hash) {
    if (!hash) {
      const wrap = document.querySelector('.content-wrap');
      if (wrap) {
        wrap.scrollTop = 0;
      }
      return;
    }

    const id = hash.replace(/^#/, '');
    if (!id) {
      return;
    }

    const target = document.getElementById(id);
    if (target) {
      target.scrollIntoView({ block: 'start' });
    }
  }

  async function navigateTo(url, pushState) {
    if (url.pathname === window.location.pathname) {
      if (url.hash === window.location.hash) {
        return;
      }
      if (pushState) {
        history.pushState(null, '', url.pathname + url.hash);
      }
      scrollToHash(url.hash);
      return;
    }

    const response = await fetch(url.pathname, { credentials: 'same-origin' });
    if (!response.ok) {
      window.location.assign(url.href);
      return;
    }

    const html = await response.text();
    const parsed = new DOMParser().parseFromString(html, 'text/html');
    syncDocumentState(parsed);

    if (pushState) {
      history.pushState(null, '', url.pathname + url.hash);
    }

    scrollToHash(url.hash);
  }

  document.addEventListener('click', (event) => {
    const anchor = findAnchor(event.target);
    if (!anchor || isModifierClick(event) || anchor.target === '_blank' || anchor.hasAttribute('download')) {
      return;
    }

    const url = new URL(anchor.href, window.location.href);
    if (!isSameOriginDocUrl(url)) {
      return;
    }

    event.preventDefault();
    // A failed soft navigation (network error, response without the doc
    // shell) must not eat the click — fall back to a full page load.
    navigateTo(url, true).catch(() => {
      window.location.assign(url.href);
    });
  });

  window.addEventListener('popstate', () => {
    navigateTo(new URL(window.location.href), false).catch(() => {
      window.location.reload();
    });
  });

  if (window.location.hash) {
    window.requestAnimationFrame(() => scrollToHash(window.location.hash));
  }
})();
