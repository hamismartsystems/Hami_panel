/* One source of truth for the theme and language controls.
 *
 * The login page and the dashboard grew these separately and drifted
 * three times in one week: the theme button named the wrong theme on
 * one of them, the logo took the wrong colour on one of them, and
 * switching language left the theme name stale on one of them. Each
 * time only the reported screen was fixed. Both screens now run this.
 *
 * It is injected into both pages rather than served as a file, because
 * the pages deliberately ship no external assets.
 */
window.HPUI = (function () {
  var THEMES = [
    { id: 'light', icon: '☀️', label: { en: 'Light', fa: 'روشن' } },
    { id: 'dark', icon: '🌙', label: { en: 'Dark', fa: 'تیره' } },
    { id: 'vdark', icon: '🌑', label: { en: 'Very dark', fa: 'خیلی تیره' } }
  ];
  var themeIdx = 1;          // dark is the default
  var lang = 'en';
  var onLang = function () {};

  function byId(id) { return document.getElementById(id); }

  /* The theme button is painted from here and nowhere else. Leaving it
     to a page's translation pass is what used to leave it showing the
     previous language until the theme happened to change. */
  function paintTheme() {
    var b = byId('themeBtn');
    if (!b) return;
    var cur = THEMES[themeIdx];
    b.textContent = cur.icon + ' ' + (cur.label[lang] || cur.label.en);
  }

  /* The button names the theme you are in, not the one you would get
     by pressing it. */
  function applyTheme(id) {
    var i = -1;
    for (var k = 0; k < THEMES.length; k++) if (THEMES[k].id === id) i = k;
    if (i >= 0) themeIdx = i;
    document.body.setAttribute('data-theme', THEMES[themeIdx].id);
    paintTheme();
    try { localStorage.setItem('hpui-theme', THEMES[themeIdx].id); } catch (e) {}
  }

  function cycleTheme() {
    applyTheme(THEMES[(themeIdx + 1) % THEMES.length].id);
  }

  function applyLang(l) {
    lang = (l === 'fa') ? 'fa' : 'en';
    var dir = lang === 'fa' ? 'rtl' : 'ltr';
    document.documentElement.lang = lang;
    document.documentElement.dir = dir;
    document.body.setAttribute('dir', dir);
    var lb = byId('langBtn');
    if (lb) lb.textContent = lang.toUpperCase();
    paintTheme();
    try { localStorage.setItem('hpui-lang', lang); } catch (e) {}
    onLang(lang);
  }

  function toggleLang() { applyLang(lang === 'en' ? 'fa' : 'en'); }

  /* start wires both buttons and restores what the operator last chose.
     The page passes onLang to run its own translation pass. */
  function start(opts) {
    onLang = (opts && opts.onLang) || function () {};
    var tb = byId('themeBtn'); if (tb) tb.onclick = cycleTheme;
    var lb = byId('langBtn'); if (lb) lb.onclick = toggleLang;

    var savedTheme = 'dark', savedLang = 'en';
    try {
      savedTheme = localStorage.getItem('hpui-theme') || 'dark';
      savedLang = localStorage.getItem('hpui-lang') || 'en';
    } catch (e) {}
    applyTheme(savedTheme);
    applyLang(savedLang);
  }

  return {
    THEMES: THEMES,
    applyTheme: applyTheme,
    cycleTheme: cycleTheme,
    applyLang: applyLang,
    toggleLang: toggleLang,
    start: start,
    get lang() { return lang; }
  };
})();

/* Kept as plain names so page code and anything poking at the console
   can still call them. */
function applyTheme(id) { HPUI.applyTheme(id); }
function applyLang(l) { HPUI.applyLang(l === undefined ? HPUI.lang : l); }
