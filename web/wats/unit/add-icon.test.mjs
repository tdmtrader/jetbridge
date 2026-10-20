// Browser-level unit tests for web/public modules. Unlike ../test (ava, a
// running Concourse), these need only headless Chrome from puppeteer:
//
//   node --test web/wats/unit/*.test.mjs
//
// The page is served from web/public over a throwaway local HTTP server, so
// the modules load exactly as the web serves them.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import puppeteer from 'puppeteer';

const publicDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../public');
const types = { '.mjs': 'text/javascript', '.js': 'text/javascript', '.html': 'text/html' };

let server, browser, page, origin;

before(async () => {
  server = http.createServer(async (req, res) => {
    const url = new URL(req.url, 'http://x');
    if (url.pathname === '/') {
      res.writeHead(200, { 'content-type': 'text/html' });
      res.end('<!doctype html><html><body></body></html>');
      return;
    }
    const file = path.join(publicDir, path.normalize(url.pathname.replace(/^\/public\//, '')));
    if (!file.startsWith(publicDir + path.sep)) { res.writeHead(404); res.end(); return; }
    try {
      const body = await fs.readFile(file);
      res.writeHead(200, { 'content-type': types[path.extname(file)] || 'application/octet-stream' });
      res.end(body);
    } catch {
      res.writeHead(404);
      res.end();
    }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  origin = `http://127.0.0.1:${server.address().port}`;
  browser = await puppeteer.launch({ headless: true });
  page = await browser.newPage();
  await page.goto(origin + '/');
});

after(async () => {
  await browser?.close();
  await new Promise(resolve => server?.close(resolve) ?? resolve());
});

// addIcon(iconName, nodeId) stores an <svg id="<nodeId>-svg-icon"> that the
// pipeline graph and the resource page <use> by id. nodeId is built from job
// and resource names (and, on the resource page, from the resource's icon:
// field), all of which pipeline config sets.
async function addIcons(calls) {
  return page.evaluate(async calls => {
    window.hits = [];
    document.getElementById('icon-store')?.remove();
    const { addIcon } = await import('/public/index.mjs');
    for (const [icon, nodeId] of calls) addIcon(icon, nodeId);
    await import('/public/mdi-svg.min.js');
    // addIcon appends once the icons module promise settles; give the
    // appended markup a beat to fire anything it carries.
    await new Promise(resolve => setTimeout(resolve, 200));
    const store = document.getElementById('icon-store');
    return {
      hits: window.hits,
      icons: [...store.children].map(el => ({
        tag: el.tagName,
        id: el.getAttribute('id'),
        attributes: [...el.attributes].map(a => a.name).sort(),
        children: [...el.children].map(c => c.tagName),
        foundById: document.getElementById(el.getAttribute('id')) === el,
      })),
    };
  }, calls);
}

test('a node id is stored as the icon id verbatim', async () => {
  const { icons } = await addIcons([['git', 'job-build-output-repo']]);
  assert.deepEqual(icons, [{
    tag: 'svg',
    id: 'job-build-output-repo-svg-icon',
    attributes: ['height', 'id', 'version', 'viewBox', 'width', 'xmlns', 'xmlns:xlink'].sort(),
    children: ['path'],
    foundById: true,
  }]);
});

test('a quote in a job or resource name adds no markup to the icon', async () => {
  const hostile = [
    `job-j-output-r" onload="window.hits.push('onload')//`,
    `job-j-output-r"><set attributeName="x" to="1" onbegin="window.hits.push('set')"/><x a="`,
    `job-j-output-r"><image href="data:," onerror="window.hits.push('image')"/><x a="`,
  ];
  const { hits, icons } = await addIcons(hostile.map(nodeId => ['git', nodeId]));
  assert.deepEqual(hits, []);
  assert.deepEqual(icons.map(i => i.id), hostile.map(nodeId => nodeId + '-svg-icon'));
  for (const icon of icons) {
    assert.equal(icon.tag, 'svg');
    assert.deepEqual(icon.children, ['path']);
    assert.ok(!icon.attributes.some(a => a.startsWith('on')), `event handler attribute on ${icon.id}`);
    assert.ok(icon.foundById);
  }
});

test('a quote in a resource icon adds no markup to the icon', async () => {
  // The resource page renders an icon with only its name, so the name is the id too.
  const icon = `x"><set attributeName="x" to="1" onbegin="window.hits.push('icon')"/><x a="`;
  const { hits, icons } = await addIcons([[icon, icon]]);
  assert.deepEqual(hits, []);
  assert.equal(icons.length, 1);
  assert.equal(icons[0].id, icon + '-svg-icon');
  assert.deepEqual(icons[0].children, ['path']);
});
