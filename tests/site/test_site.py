"""Unit tests of leadeck.dev (#363): the Lock Family site kit, its links, its content rules.

    python3 -m unittest discover -s tests/site -p 'test_*.py'

The site is assembled first (tools/site/assemble.sh, as pages.yml publishes it), so the links
are checked against what GitHub Pages serves.
"""

import hashlib
import html.parser
import os
import re
import shutil
import subprocess
import tempfile
import unittest
import urllib.parse

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
SITE = os.path.join(ROOT, 'site')
DOMAIN = 'https://leadeck.dev'

# page file -> (canonical path, nav entry marked aria-current, depth prefix)
PAGES = {
    'index.html': ('/', 'Home', ''),
    'install/index.html': ('/install/', 'Install', '../'),
    'how-it-works/index.html': ('/how-it-works/', 'How it works', '../'),
    'security/index.html': ('/security/', 'Security', '../'),
    'faq/index.html': ('/faq/', 'FAQ', '../'),
    '404.html': ('/404.html', None, '/'),
}
NAV = ['Home', 'Install', 'How it works', 'Security', 'FAQ', 'GitHub']
FAMILY = ['locksql', 'lockmq', 'lockfile', 'lockhttp', 'locklog', 'lockssh', 'lockhub', 'lockkit', 'lea']

# The kit's shared files, byte for byte as lockfamily ships them (lockfamily c58cac8:
# site-kit/assets/, assets/tokens.css, assets/fonts/, assets/lea.svg). Never edit them in lea:
# change them in lockfamily, copy them here, then update these hashes.
KIT = {
    'assets/lock-site.css': 'b242351c1ade49409d46cc8dbcc4c0a309b75fc8e07894d6cb29fad475241bbe',
    'assets/copy.js': '878219924a4c11c0c264e0cc5f675f81648cf7f2af22a50c4b9e42a8b3938a62',
    'assets/tokens.css': '6ae81028f853ea660d885a7b6bc2db151fc7eab23197bb9d6709a64d994a8ebe',
    'assets/logo.svg': 'f997cefee8926ee94d4834b30559d6643133d50880d53dec0c2d2e7bd585c4b9',
    'assets/fonts/OFL-ibm-plex-mono.txt': '7e6b2818edbd8f6a01ae80641cc8f16a51080d08fb4e532be3a0b6f74adb07da',
    'assets/fonts/OFL-source-serif-4.txt': '5f94c3fd3a23131a417ab5a0c8452de57e70c3cfb9f604d88241f7065ebf9fd9',
    'assets/fonts/OFL-zilla-slab.txt': '240b385d3ec8c8d5c1d4af2bbba6ac72bac0309a80c1a8ff112c168643cc6811',
    'assets/fonts/ibm-plex-mono-400.woff2': '3c5a451f9ec27a354b0c2bcca636c6ec17a651281aabf29f8427e210a1d31e85',
    'assets/fonts/ibm-plex-mono-600.woff2': 'c4d3deb734a27e6d0dc7a6b464779f70ba1c272e26287860a14e35e85acb5b76',
    'assets/fonts/source-serif-4-400-italic.woff2': 'b9ca45ecab30d27712ffbd86c256adc615943e05a6dd7cb52f5f4241cab45299',
    'assets/fonts/source-serif-4-400.woff2': '5e7fda9cec061fcef363f78ccd099988e89683633a3ed46bf0bfb460e7246a2c',
    'assets/fonts/source-serif-4-600.woff2': '2847f4d3b333168cf9254cbdecb8b7735c8663469e701deab452b34c16e9092e',
    'assets/fonts/zilla-slab-700.woff2': 'e7164ba121877a43c5346ed4a1ff1d79db1e47c742839653db30635bae86d171',
}


def read(rel, base=SITE):
    with open(os.path.join(base, rel), encoding='utf-8') as fh:
        return fh.read()


class Links(html.parser.HTMLParser):
    """Collects ids, local href/src targets and the resources a page loads."""

    def __init__(self):
        super().__init__()
        self.ids, self.links, self.resources = set(), [], []

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if 'id' in a:
            self.ids.add(a['id'])
        for name in ('href', 'src', 'srcset'):
            if name in a:
                self.links.append(a[name])
                if tag in ('link', 'script', 'img', 'source', 'video', 'iframe') and a.get('rel') != 'canonical':
                    self.resources.append(a[name])


def parse(text):
    p = Links()
    p.feed(text)
    return p


class Site(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.mkdtemp(prefix='lea-site-')
        cls.out = os.path.join(cls.tmp, '_site')
        subprocess.run(['sh', os.path.join(ROOT, 'tools/site/assemble.sh'), cls.out], check=True)
        cls.html = {p: read(p) for p in PAGES}

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.tmp)

    # ---- the kit ----

    def test_kit_files_are_lockfamilys_bytes(self):
        for rel, digest in KIT.items():
            with open(os.path.join(SITE, rel), 'rb') as fh:
                self.assertEqual(hashlib.sha256(fh.read()).hexdigest(), digest,
                                 '%s differs from lockfamily: copy it again, never edit it here' % rel)
        fonts = sorted(os.listdir(os.path.join(SITE, 'assets/fonts')))
        self.assertEqual(fonts, sorted(os.path.basename(k) for k in KIT if k.startswith('assets/fonts/')))

    def test_pages_and_their_files(self):
        for rel in list(PAGES) + ['robots.txt', 'sitemap.xml', 'CNAME', 'assets/anchors.js',
                                  'assets/og.png', 'assets/apple-touch-icon.png']:
            self.assertTrue(os.path.isfile(os.path.join(SITE, rel)), rel)
        self.assertEqual(read('CNAME'), 'leadeck.dev\n')
        locs = re.findall(r'<loc>([^<]+)</loc>', read('sitemap.xml'))
        self.assertEqual(locs, [DOMAIN + PAGES[p][0] for p in PAGES if p != '404.html'])
        self.assertIn('Sitemap: %s/sitemap.xml' % DOMAIN, read('robots.txt'))
        with open(os.path.join(SITE, 'assets/og.png'), 'rb') as fh:
            head = fh.read(24)
        self.assertEqual(head[:8], b'\x89PNG\r\n\x1a\n')
        self.assertEqual((int.from_bytes(head[16:20], 'big'), int.from_bytes(head[20:24], 'big')), (1200, 630))
        with open(os.path.join(SITE, 'assets/apple-touch-icon.png'), 'rb') as fh:
            head = fh.read(24)
        self.assertEqual((int.from_bytes(head[16:20], 'big'), int.from_bytes(head[20:24], 'big')), (180, 180))
        for gone in ('assets/style.css', 'og.html'):
            self.assertFalse(os.path.exists(os.path.join(SITE, gone)), gone)

    def test_every_page_has_the_kits_head(self):
        for page, (path, _, root) in PAGES.items():
            h = self.html[page]
            self.assertTrue(h.startswith('<!doctype html>\n<html lang="en">\n<head>\n'), page)
            self.assertNotIn('{{', h, page)
            url = DOMAIN + path
            for line in ('<link rel="canonical" href="%s">' % url,
                         '<meta property="og:url" content="%s">' % url,
                         '<meta property="og:image" content="%s/assets/og.png">' % DOMAIN,
                         '<meta name="twitter:card" content="summary_large_image">',
                         '<meta name="color-scheme" content="light dark">',
                         '<meta name="theme-color" content="#f4efe4" media="(prefers-color-scheme: light)">',
                         '<meta name="theme-color" content="#16140f" media="(prefers-color-scheme: dark)">',
                         '<link rel="icon" href="%sassets/logo.svg" type="image/svg+xml">' % root,
                         '<link rel="apple-touch-icon" href="%sassets/apple-touch-icon.png">' % root,
                         '<link rel="preload" href="%sassets/fonts/zilla-slab-700.woff2" as="font" type="font/woff2" crossorigin>' % root,
                         '<script src="%sassets/copy.js" defer></script>' % root):
                self.assertIn(line, h, page)
            self.assertLess(h.index('href="%sassets/tokens.css"' % root), h.index('href="%sassets/lock-site.css"' % root), page)
            title = re.search(r'<title>([^<]+)</title>', h).group(1)
            self.assertIn('<meta property="og:title" content="%s">' % title, h, page)
            desc = re.search(r'<meta name="description" content="([^"]+)">', h).group(1)
            self.assertLessEqual(len(desc), 250, page)

    def test_every_page_has_the_kits_header(self):
        for page, (_, current, root) in PAGES.items():
            h = self.html[page]
            header = h[h.index('<a class="skip-link" href="#main">'):h.index('</header>')]
            self.assertIn('<span class="wordmark">lea</span>', header)
            self.assertIn('<img src="%sassets/logo.svg" alt="" width="32" height="32">' % root, header)
            labels = re.findall(r'<li><a href="[^"]+"(?: aria-current="page")?>([^<]+)</a></li>', header)
            self.assertEqual(labels, NAV, page)
            marked = re.findall(r'aria-current="page">([^<]+)<', header)
            self.assertEqual(marked, [current] if current else [], page)
            self.assertNotIn('data-nav', header, page)
            self.assertIn('<main id="main" tabindex="-1">', h, page)

    def test_every_page_has_the_kits_footer(self):
        footers = set()
        for page, (_, _, root) in PAGES.items():
            h = self.html[page]
            footer = h[h.index('<footer class="site-footer">'):h.index('</footer>')]
            strip = footer[footer.index('<nav class="family-strip"'):]
            self.assertEqual(re.findall(r'<li data-lock="([a-z]+)"', strip), FAMILY, page)
            self.assertEqual(re.findall(r'<li data-lock="([a-z]+)" aria-current="true">', strip), ['lea'], page)
            for name in FAMILY:
                self.assertIn('<a href="https://github.com/DavidGodefroid/%s">' % name, strip)
            self.assertIn('Claude and Claude Code are trademarks of Anthropic.', footer, page)
            self.assertIn('Part of the <a href="https://lockfamily.dev">Lock Family</a>', footer, page)
            footers.add(footer.replace('href="%s' % (root or './'), 'href="ROOT').replace('src="%s' % root, 'src="ROOT'))
        self.assertEqual(len(footers), 1, 'one footer on every page')

    def test_inner_pages_have_an_eyebrow(self):
        for page in PAGES:
            if page == 'index.html':
                continue
            main = self.html[page][self.html[page].index('<main'):]
            self.assertRegex(main, r'^<main id="main" tabindex="-1">\s*<div class="wrap">\s*<p class="eyebrow">[^<]+</p>\s*<h1>', page)

    def test_home_hero_is_the_kits(self):
        h = self.html['index.html']
        hero = h[h.index('<section class="hero"'):h.index('</section>')]
        self.assertIn('<h1 id="hero-title">Many agents. <span class="key">None of your machine.</span></h1>', hero)
        self.assertIn('class="hero-aside ticket"', hero)
        self.assertIn('<p class="hub-line">Part of the <a href="https://lockfamily.dev">Lock Family</a>: one hub (<code>lox run</code>)', hero)
        box = ('<pre tabindex="0" aria-label="Install commands"><code><span class="cmd">brew trust davidgodefroid/tap</span>\n'
               '<span class="cmd">brew install davidgodefroid/tap/lox davidgodefroid/tap/lea</span></code></pre>')
        self.assertIn(box, hero)
        self.assertIn(box, self.html['install/index.html'])
        self.assertNotIn('<button', h)  # copy.js adds the Copy button

    # ---- links ----

    def test_local_links_resolve(self):
        # Every local href/src and #fragment resolves in the assembled site.
        ids = {}
        for dirpath, _, files in os.walk(self.out):
            for f in files:
                if f.endswith('.html'):
                    rel = os.path.relpath(os.path.join(dirpath, f), self.out)
                    ids[rel] = parse(read(rel, self.out)).ids
        for page in PAGES:
            for link in parse(self.html[page]).links:
                u = urllib.parse.urlsplit(link)
                if u.scheme or u.netloc:
                    continue
                if not u.path:
                    target = page
                elif u.path.startswith('/'):
                    target = os.path.normpath(u.path.lstrip('/') or '.')
                else:
                    target = os.path.normpath(os.path.join(os.path.dirname(page), u.path))
                if os.path.isdir(os.path.join(self.out, target)):
                    target = os.path.normpath(os.path.join(target, 'index.html'))
                full = os.path.join(self.out, target)
                self.assertTrue(os.path.isfile(full), '%s: %s' % (page, link))
                if u.fragment:
                    self.assertIn(u.fragment, ids.get(target, set()), '%s: %s' % (page, link))

    def test_readme_anchors_exist(self):
        # Links into lea's own README point at headings it has.
        readme = read('README.md', ROOT)
        slugs = set()
        for title in re.findall(r'^#+ (.+)$', readme, re.M):
            slug = re.sub(r'[^\w\- ]', '', title.lower().replace('`', '')).replace(' ', '-')
            slugs.add(slug)
        for page in PAGES:
            for anchor in re.findall(r'github\.com/DavidGodefroid/lea/blob/main/README\.md#([\w-]+)', self.html[page]):
                self.assertIn(anchor, slugs, page)
            for rel in re.findall(r'github\.com/DavidGodefroid/lea/blob/main/([\w./-]+?)(?:#|")', self.html[page]):
                self.assertTrue(os.path.exists(os.path.join(ROOT, rel)), (page, rel))

    def test_no_third_party_resource(self):
        for page in PAGES:
            for url in parse(self.html[page]).resources:
                self.assertFalse(re.match(r'^[a-z]+:|^//', url), (page, url))

    def test_assemble_keeps_the_sites_assets(self):
        for dirpath, _, files in os.walk(SITE):
            for f in files:
                rel = os.path.relpath(os.path.join(dirpath, f), SITE)
                with open(os.path.join(SITE, rel), 'rb') as a, open(os.path.join(self.out, rel), 'rb') as b:
                    self.assertEqual(a.read(), b.read(), rel)
        for rel in ('assets/demo.svg', 'install.sh'):
            self.assertTrue(os.path.isfile(os.path.join(self.out, rel)), rel)
        self.assertIn('tools/site/assemble.sh _site', read('.github/workflows/pages.yml', ROOT))

    # ---- the identity: nothing of the old look ----

    def test_no_old_look(self):
        old = ('catppuccin', '--crust', '--mantle', '--peach', '--lav', '#11111b', '#1e1e2e', '#cdd6f4',
               'inter,', "'inter'", 'family=inter', 'jetbrains mono', 'jetbrains+mono',
               'fonts.googleapis.com', 'fonts.gstatic.com', 'favicon.svg', 'social-card.png')
        for dirpath, _, files in os.walk(SITE):
            for f in files:
                if f.endswith(('.html', '.css', '.js', '.svg', '.txt', '.xml')):
                    text = read(os.path.relpath(os.path.join(dirpath, f), SITE)).lower()
                    for w in old:
                        self.assertNotIn(w, text, os.path.join(dirpath, f))
        for rel in ('assets/favicon.svg', 'assets/social-card.png', 'assets/demo.gif', 'tools/brand/render.py'):
            self.assertFalse(os.path.exists(os.path.join(ROOT, rel)), rel)

    # ---- content rules ----

    def test_leads_are_short(self):
        for page in PAGES:
            for lead in re.findall(r'<p class="lead[^"]*">(.*?)</p>', self.html[page], re.S):
                text = re.sub(r'<[^>]+>', '', lead)
                self.assertLessEqual(len(re.findall(r'[.!?](\s|$)', text)), 2, (page, text))

    def test_agent_neutral(self):
        everything = ''.join(self.html.values())
        self.assertNotIn('Claude Code agents', everything)
        for w in ('Claude Code', 'Codex', 'Ollama'):
            self.assertIn(w, self.html['index.html'] + self.html['faq/index.html'], w)
        meta = re.search(r'<meta name="description" content="([^"]+)">', self.html['index.html']).group(1)
        self.assertIn('Codex', meta)

    def test_facts(self):
        everything = ''.join(self.html.values())
        self.assertNotIn('lox up', everything)
        self.assertNotIn('lox approve', everything)
        self.assertNotIn('lox init', everything)
        self.assertNotIn('0.5.0', everything)
        self.assertIn('<code>lox</code> 0.9.0 or later', self.html['install/index.html'])
        for page in ('index.html', 'install/index.html', 'faq/index.html', 'how-it-works/index.html'):
            self.assertIn('<code>lox run</code>', self.html[page], page)
        install = self.html['install/index.html']
        for lock in ('locksql', 'lockmq', 'lockfile', 'locklog', 'lockhttp'):
            self.assertIn(lock, install, lock)
        self.assertIn('LEA_LOCKHTTP=1', install)
        self.assertIn('curl -fsSL https://leadeck.dev/install.sh | sh', install)

    def test_v0_15_facts(self):
        everything = ''.join(self.html.values())
        # lea allow replaced lea delivery and lea grant (#319)
        for old in ('lea delivery', 'lea grant'):
            self.assertNotIn(old, everything)
        self.assertNotIn('v0.14', everything)
        for page in PAGES:
            if page != '404.html':
                self.assertIn('Pre-release (v0.15.x)', self.html[page], page)
        home, how = self.html['index.html'], self.html['how-it-works/index.html']
        self.assertIn('<code>lea allow deliver</code>', home)
        self.assertIn('<code>lea allow deliver</code>', self.html['faq/index.html'])
        # the first agent is a Control Tower (#339), one question at a time (#95)
        self.assertIn('The first agent is a Control Tower', home)
        self.assertIn('one question at a time', home)
        self.assertIn('<h2 id="tower">The Control Tower</h2>', how)
        self.assertIn('<h2 id="allowances">Allowances</h2>', how)
        for key, what in (('N', 'Control Tower'), ('Q', 'questions'), ('H', 'pause'), ('g', 'allowed'), ('T', 'theme')):
            self.assertRegex(how, r'<kbd>%s</kbd>[^\n]*</td><td>[^<]*%s' % (key, what), key)
        self.assertIn('<code>lea config claude</code>', how)
        self.assertIn('<code>statusLine</code>', how)
        self.assertIn('works where the lead works', how)
        self.assertIn('A DNS resolver', self.html['security/index.html'])

    def test_limits(self):
        self.assertIn('<h2 id="limits">Know the limits</h2>', self.html['index.html'])
        self.assertIn('<section class="limits"', self.html['security/index.html'])

    def test_faq_markup(self):
        faq = self.html['faq/index.html']
        body = faq[faq.index('<div class="faq prose">'):]
        self.assertGreaterEqual(body.count('<section aria-labelledby='), 8)

    def test_links_go_to_repos(self):
        for page in PAGES:
            for site in ('locksql.com', 'lockmq.com', 'lockfile.pro', 'lockhttp.com', 'locklog.dev'):
                self.assertNotRegex(self.html[page], r'href="https?://(www\.)?%s' % re.escape(site), (page, site))

    def test_no_emoji(self):
        for page in PAGES:
            self.assertIsNone(re.search('[\U0001F300-\U0001FAFF☀-⛿]', self.html[page]), page)

    def test_brand_doc_points_to_the_family(self):
        brand = read('assets/BRAND.md', ROOT)
        for w in ('docs/identity.md', 'site-kit', 'tokens.css', 'Zilla Slab', 'Source Serif 4', 'IBM Plex Mono'):
            self.assertIn(w, brand)
        self.assertNotIn('Inter for text', brand)


if __name__ == '__main__':
    unittest.main()
