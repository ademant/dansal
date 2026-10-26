// Extracted from base.html's inline <script> blocks (#1217) — the
// data-fn dispatcher, shared helper functions, and help-modal logic
// used across every page. Cached by the browser across page
// navigations instead of being re-downloaded as inline HTML.
//
// Loaded with defer (#1326) — see base.html's script tag for why this is
// now safe (it wasn't when #1217 first wrote this file: several inline,
// non-deferred page scripts called functions here expecting them to
// already exist, which a deferred load would have broken).

// --- Delegated event-handler dispatcher (#1149) ---
// Elements use data-fn/data-args instead of inline onclick=/onchange=/
// onsubmit=/oninput=/onkeydown=/onfocus=/onload= so CSP can drop
// 'unsafe-inline' from script-src once #1141 flips the header (a nonce in
// script-src makes CSP-compliant browsers ignore 'unsafe-inline' entirely,
// so those attributes would otherwise silently stop firing). One
// document-level, capture-phase listener per event kind replaces every
// inline handler (capture phase works even for non-bubbling kinds like
// focus/load, since capture always travels down to the target regardless
// of bubbling); attributes:
//   data-fn="name"        function on window to call (dotted paths like
//                          "obj.method" are resolved against window)
//   data-args='[...]'     JSON array of arguments (built by the `dargs`
//                          template func); "@this"/"@value"/"@event" tokens
//                          are replaced with the element/its value/the event
//   data-confirm="text"   confirm() first; abort (and preventDefault) if declined
//   data-stop             call event.stopPropagation() (independent of
//                          data-on — matches a separately-attached ancestor
//                          listener, e.g. a table row's own click handler)
//   data-if-value         only dispatch when the element's value is truthy
//                          (the onchange="if(this.value)fn(...)" pattern)
//   data-self-only         only dispatch when the event target IS this element,
//                          not a descendant (modal-backdrop click-outside-closes)
//   data-on="change|submit|input|keydown|focus|load" dispatch data-fn/
//                          data-confirm on that event kind instead of the
//                          default "click" (data-stop always
//                          applies to click regardless of data-on)
function csp1149DispatchOne(el, evt, kind, suf) {
  var onAttr = el.getAttribute('data-on' + suf);
  var on = onAttr || (suf === '' ? 'click' : null);
  if (!on || on.split(',').indexOf(kind) === -1) return;
  if (el.hasAttribute('data-self-only') && evt.target !== el) return;
  if (el.hasAttribute('data-if-value') && !el.value) return;
  var confirmMsg = el.getAttribute('data-confirm' + suf);
  if (confirmMsg !== null && !confirm(confirmMsg)) {
    evt.preventDefault();
    return;
  }
  var fnName = el.getAttribute('data-fn' + suf);
  if (!fnName) return;
  var fn = fnName.split('.').reduce(function (o, k) { return o ? o[k] : undefined; }, window);
  if (typeof fn !== 'function') { console.error('csp1149Dispatch: unknown function', fnName); return; }
  var args = [];
  var raw = el.getAttribute('data-args' + suf);
  try { args = raw ? JSON.parse(raw) : []; } catch (err) { args = []; }
  args = args.map(function (a) {
    if (a === '@this') return el;
    if (a === '@value') return el.value;
    if (a === '@event') return evt;
    return a;
  });
  var result = fn.apply(el, args);
  if (kind === 'submit' && result === false) evt.preventDefault();
}
// A single element can carry up to three independent dispatch rules
// (data-fn/data-args/data-on, then data-fn2/data-args2/data-on2, then
// .../3) for the rare case where different event kinds on the same element
// need different functions or arguments (e.g. oninput vs onfocus vs
// onkeydown on one autocomplete box). data-stop/data-self-only/data-if-value
// are shared guards, not per-rule.
function csp1149Dispatch(el, evt, kind) {
  if (el.hasAttribute('data-stop')) evt.stopPropagation();
  ['', '2', '3'].forEach(function (suf) { csp1149DispatchOne(el, evt, kind, suf); });
}
['click', 'change', 'submit', 'input', 'keydown', 'focus', 'load'].forEach(function (kind) {
  document.addEventListener(kind, function (e) {
    // e.target isn't always an Element with .closest(): some browsers fire
    // events targeting document/window itself (observed with <input
    // type=color>'s native picker), and .closest doesn't exist on those —
    // throwing here would silently swallow this one event's dispatch
    // (the exception aborts this listener before reaching the data-fn
    // button the click was actually meant for), not just this diagnostic.
    if (!(e.target instanceof Element)) return;
    var el = e.target.closest('[data-fn],[data-stop],[data-confirm]');
    if (el) csp1149Dispatch(el, e, kind);
  }, true);
});
// navigateTo: small data-fn helper for whole-card/whole-row "click to open"
// elements (e.g. the mobile map popup in index.html).
function navigateTo(url) { window.location = url; }
// submitParentForm: data-fn helper for the very common auto-submit-filter
// pattern (data-on="change" data-fn="submitParentForm" data-args='["@this"]').
function submitParentForm(el) { el.form.submit(); }
function scrollToTop() { window.scrollTo({top: 0, behavior: 'smooth'}); }
// clickElement: data-fn helper for onclick="document.getElementById(id).click()".
function clickElement(id) { document.getElementById(id).click(); }
// hideParentElement: data-fn helper for onclick="this.parentElement.hidden=true".
function hideParentElement(el) { el.parentElement.hidden = true; }
// shareURL: data-fn helper for a share button — Web Share API where available
// (opens the native OS share sheet), else copies the link to the clipboard
// with a brief checkmark confirmation on the triggering button (same pattern
// as the copy-link buttons on admin_series_edit.html/admin_fetchurl_edit.html).
function shareURL(url, btn) {
  if (navigator.share) { navigator.share({url: url}).catch(function(){}); return; }
  if (navigator.clipboard) {
    navigator.clipboard.writeText(url).then(function(){
      var orig = btn.textContent;
      btn.textContent = '✓';
      setTimeout(function(){ btn.textContent = orig; }, 1500);
    });
  }
}
// closeBulkAttrsDialog: shared by the admin events/locations/org bulk-attrs modal.
function closeBulkAttrsDialog() { document.getElementById('bulk-attrs-dialog').close(); }
// updateActorPreview (#1647): shows the Fediverse handle an organisation name
// will get on the registration form, as the applicant types. The host comes
// from data-host on the preview element.
function updateActorPreview(v) {
  var out = document.getElementById('actor-preview');
  if (!out) return;
  var host = out.getAttribute('data-host') || '';
  v = (v || '').trim();
  out.textContent = v && host ? '@' + v + '@' + host : '';
}

// toggleKuferFields: shared by the fetchurl new/edit forms' "type" select.
function toggleKuferFields(val) {
  document.getElementById('kufer-fields').style.display = val === 'kufer' ? '' : 'none';
}
function closeWindow() { window.close(); }

// --- New menu toggle functions ---
function toggleNavMenu(){
  var dropdown=document.getElementById('nav-dropdown');
  var isOpen=!dropdown.hidden;
  dropdown.hidden=isOpen;
  // Close user dropdown when opening nav
  if(!isOpen) document.getElementById('user-dropdown').hidden=true;
}
function toggleUserMenu(){
  var dropdown=document.getElementById('user-dropdown');
  var isOpen=!dropdown.hidden;
  dropdown.hidden=isOpen;
  // Close nav dropdown when opening user
  if(!isOpen) document.getElementById('nav-dropdown').hidden=true;
}
// Close dropdowns when clicking outside
document.addEventListener('click',function(e){
  // e.target isn't always an Element with .closest() — see the capture-phase
  // dispatcher above for why (observed with <input type=color>'s native
  // picker); guard the same way here.
  if(!(e.target instanceof Element) || !e.target.closest('nav')){
    document.getElementById('nav-dropdown').hidden=true;
    document.getElementById('user-dropdown').hidden=true;
  }
});
// Close on Escape key
document.addEventListener('keydown',function(e){
  if(e.key==='Escape'){
    document.getElementById('nav-dropdown').hidden=true;
    document.getElementById('user-dropdown').hidden=true;
    document.getElementById('info-popup').hidden=true;
  }
});

function initSortable(tbl){
  var ths=tbl.querySelectorAll('thead th[data-sort]');
  ths.forEach(function(th){
    th.classList.add('sortable-th');
    th.addEventListener('click',function(){
      var asc=th.dataset.sortDir!=='asc';
      ths.forEach(function(t){t.dataset.sortDir='';var i=t.querySelector('.sort-ind');if(i)i.textContent='';});
      th.dataset.sortDir=asc?'asc':'desc';
      var ind=th.querySelector('.sort-ind');
      if(!ind){ind=document.createElement('span');ind.className='sort-ind';th.appendChild(ind);}
      ind.textContent=asc?' ▲':' ▼';
      var tbody=tbl.querySelector('tbody');
      var rows=Array.prototype.slice.call(tbody.querySelectorAll('tr'));
      var allThs=Array.prototype.slice.call(tbl.querySelectorAll('thead th'));
      var col=allThs.indexOf(th);
      var type=th.dataset.sort;
      rows.sort(function(a,b){
        var av=a.cells[col]?(a.cells[col].dataset.val||a.cells[col].textContent.trim()):'';
        var bv=b.cells[col]?(b.cells[col].dataset.val||b.cells[col].textContent.trim()):'';
        if(type==='num'){return asc?(+av||0)-(+bv||0):(+bv||0)-(+av||0);}
        return asc?av.localeCompare(bv):bv.localeCompare(av);
      });
      rows.forEach(function(r){tbody.appendChild(r);});
    });
  });
}
function toggleHamburger(btn){
  var nav=btn.closest('nav');
  var open=nav.classList.toggle('nav-open');
  btn.textContent=open?'✕':'☰';
  btn.setAttribute('aria-expanded',open);
}
function toggleInfoPopup(btn){
  var p=btn.nextElementSibling;
  var open=!p.hidden;
  p.hidden=open;
  if(!open){document.addEventListener('click',function h(e){if(!btn.parentElement.contains(e.target)){p.hidden=true;document.removeEventListener('click',h);}},{once:false,capture:false});}
}
// #1582: print. printPage backs the user-menu item and the agenda button. On
// pages with a [data-print-qr] marker (org, location) a QR code of the page URL
// is added for the print only, right after the marker. The URL is also printed
// as text, but only on pages without a query string; filtered views get the QR
// alone. If the QR library is missing (Save-Data stub) the page still prints.
function printPage(){window.print();}
function addPrintQR(){
  var marker=document.querySelector('[data-print-qr]');
  if(!marker||typeof QRCode==='undefined'||document.getElementById('print-qr'))return;
  try{
    var box=document.createElement('div');
    box.className='print-qr';
    box.id='print-qr';
    var base=location.origin+location.pathname;
    if(!location.search){
      var txt=document.createElement('p');
      txt.className='print-qr-url';
      txt.textContent=base;
      box.appendChild(txt);
    }
    var code=document.createElement('div');
    new QRCode(code,{text:base+location.search,width:110,height:110,correctLevel:QRCode.CorrectLevel.M});
    box.appendChild(code);
    marker.parentNode.insertBefore(box,marker.nextSibling);
  }catch(e){}
}
function removePrintQR(){var b=document.getElementById('print-qr');if(b)b.remove();}
window.addEventListener('beforeprint',addPrintQR);
window.addEventListener('afterprint',removePrintQR);
// #1589: PWA. The service worker (sw.js) lets the site open offline. The
// offline class greys the page and shows the banner while there is no
// connection. The install item shows only when the browser offers to install.
if('serviceWorker' in navigator){
  window.addEventListener('load',function(){navigator.serviceWorker.register('/sw.js').catch(function(){});});
}
function setOfflineState(){document.documentElement.classList.toggle('offline',!navigator.onLine);}
window.addEventListener('online',setOfflineState);
window.addEventListener('offline',setOfflineState);
setOfflineState();
var deferredInstall=null;
function showInstallItem(show){var b=document.getElementById('install-app-item');if(b)b.hidden=!show;}
window.addEventListener('beforeinstallprompt',function(e){e.preventDefault();deferredInstall=e;showInstallItem(true);});
window.addEventListener('appinstalled',function(){deferredInstall=null;showInstallItem(false);});
function installApp(){
  if(!deferredInstall)return;
  var p=deferredInstall;
  deferredInstall=null;
  showInstallItem(false);
  p.prompt();
}
function cycleTheme(){
  var h=document.documentElement;
  var cur=localStorage.getItem('colorScheme')||'auto';
  var next=cur==='auto'?'dark':cur==='dark'?'light':'auto';
  localStorage.setItem('colorScheme',next);
  h.classList.remove('dark','light');
  if(next==='dark')h.classList.add('dark');
  else if(next==='light')h.classList.add('light');
}
// Tiles are proxied and disk-cached through dansal_web itself (#1079) rather
// than fetched by the browser directly from OSM — both a tile usage policy
// requirement (OSM forbids heavy direct use by distributed apps) and a
// privacy win (visitor IPs never reach a third-party tile server).
// #1169: dark mode used to switch to a separate CARTO "dark_all" upstream,
// which now requires a registered API key and serves an "API key required"
// overlay without one. Dark mode now reuses these same OSM tiles and is
// darkened purely with a CSS filter on the tile pane (see base.html's
// .leaflet-tile-pane rule) — no second upstream, no swap needed on theme
// change.
function makeTileLayer(){
  // #1269: the tile proxy requires the instance's public token (query
  // param — Leaflet's default tile layer loads tiles as plain <img>
  // requests, which can't carry a custom Authorization header) or a real
  // API key, to stop the endpoint being used as an open, unmetered OSM
  // tile mirror by anyone else. DANSAL_TILE_TOKEN is emitted by base.html
  // just above this script's own <script src> tag.
  var tileURL = '/tiles/osm/{z}/{x}/{y}.png';
  if(typeof DANSAL_TILE_TOKEN !== 'undefined' && DANSAL_TILE_TOKEN) tileURL += '?t=' + encodeURIComponent(DANSAL_TILE_TOKEN);
  return L.tileLayer(tileURL,{
    attribution:'© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a>',
    maxZoom:18});
}
// Nominatim address results are re-fetched in the venue's own country's
// language, so a German venue always gets "München"/"Bayern" etc. regardless
// of the admin's own UI language or the browser's locale (#1126). Keyed on
// ISO 3166-1 alpha-2 country code; falls back to English for anything not
// listed rather than guessing a language.
var NOMINATIM_REGION_LANG = {
  DE:'de', AT:'de', CH:'de,fr,it', LI:'de',
  FR:'fr', BE:'nl,fr', LU:'fr,de', MC:'fr',
  NL:'nl', ES:'es', PT:'pt', AD:'ca',
  IT:'it', SM:'it', VA:'it',
  DK:'da', SE:'sv', NO:'no', FI:'fi,sv',
  PL:'pl', CZ:'cs', SK:'sk', HU:'hu', RO:'ro'
};
function nominatimLang(countryCode){
  return NOMINATIM_REGION_LANG[(countryCode||'').toUpperCase()] || 'en';
}
// nominatimSearch/nominatimReverse: proxy through dansal_web's own
// /search/geocode/search and /search/geocode/reverse endpoints instead of
// calling nominatim.openstreetmap.org directly from the browser (#1313) — a
// browser fetch() can't set the User-Agent Nominatim's usage policy
// requires (it's a forbidden header), and independently-loaded pages have
// no way to coordinate the shared ~1 request/second pacing that policy also
// requires. The server-side proxy does both, same as it already did for
// search.html's town-radius geocoding. Response shape is untouched
// (Nominatim's own JSON passed straight through), so callers parse it
// exactly as before.
// _geoToken gates /search/geocode/search for pages with no session (#1314)
// — a public page sets it once via setGeoToken() with its page-rendered
// GeoToken value, then keeps it current via its own existing _form_token
// background-refresh cycle (both ride the same /api-internal/refresh-
// form-token response). Admin pages never call setGeoToken; the endpoint
// accepts their logged-in session instead, so leaving this blank there is
// correct, not an oversight.
var _geoToken = '';
function setGeoToken(tok){ _geoToken = tok || ''; }

// keepFormTokensFresh (#1211, shared since #1430) keeps the hidden
// _form_token inputs of a long-open public form valid: every 10 minutes, and
// once when the tab becomes visible again, it fetches a fresh token for each
// input (tokens are single-use, so each form needs its own). The timer does
// nothing while the tab is hidden — forgotten background tabs used to poll
// for days — and the refresh on return covers the gap. onRefresh(d), if
// given, receives the first response of each run (e.g. for d.geo_token).
// Fails soft: on any error the old token stays.
function keepFormTokensFresh(inputs, onRefresh){
  inputs = Array.prototype.filter.call(inputs || [], Boolean);
  if (!inputs.length && !onRefresh) return;
  var last = 0;
  function refresh(){
    if (document.visibilityState === 'hidden') return;
    var now = Date.now();
    if (now - last < 10000) return;
    last = now;
    var n = Math.max(inputs.length, 1);
    for (var i = 0; i < n; i++) (function(el, first){
      fetch('/api-internal/refresh-form-token', {credentials:'same-origin'})
        .then(function(r){ return r.ok ? r.json() : null; })
        .then(function(d){
          if (!d) return;
          if (el && d.token) el.value = d.token;
          if (first && onRefresh) onRefresh(d);
        })
        .catch(function(){});
    })(inputs[i], i === 0);
  }
  document.addEventListener('visibilitychange', function(){ if (document.visibilityState === 'visible') refresh(); });
  setInterval(refresh, 10 * 60 * 1000);
}

// tzNote (#1392, de-duplicated #1404): builds the "N <fallback label>, N
// <unparsed label>" warning suffix appended to a fetch-source run toast when
// a feed carried events with unresolvable TZIDs or events that couldn't be
// read at all — appended only when non-zero, so the common clean-import case
// is unchanged. fallbackLabel/unparsedLabel are the already-translated label
// text: this file is served as a static asset with no access to Go's i18n,
// so each admin_fetchurl* template still does its own tiny
// {{$.Strings.T "..."}} interpolation at the call site and passes the
// result in, instead of duplicating this whole function body per template.
function tzNote(d, fallbackLabel, unparsedLabel){
  var parts = [];
  if (d.timezone_fallback) parts.push(d.timezone_fallback + ' ' + fallbackLabel);
  if (d.unparsed) parts.push(d.unparsed + ' ' + unparsedLabel);
  return parts.length ? ', ⚠ ' + parts.join(', ') : '';
}
// nominatimSearch: one lookup. Pasted venue addresses often start with the
// venue name ("Aurachsaal, Gaisbühlstr. 4, Erlangen"), which Nominatim does not
// match. So when the query finds nothing and has several comma-separated parts,
// it retries once without the first part. cb(results, meta): meta.strippedPrefix
// is the dropped part when the retry found something. Callers that do not use
// it ignore the second argument. At most one extra request, and only on an
// empty result (#1650).
function nominatimSearch(q, lang, limit, cb){
  function ask(query, next){
    var url = '/search/geocode/search?q=' + encodeURIComponent(query) + '&limit=' + (limit || 5);
    if (lang) url += '&lang=' + encodeURIComponent(lang);
    if (_geoToken) url += '&geo_token=' + encodeURIComponent(_geoToken);
    fetch(url).then(function(r){ return r.json(); }).then(function(data){
      next(data || []);
    }).catch(function(){ next([]); });
  }
  var parts = String(q).split(',').map(function(p){ return p.trim(); }).filter(Boolean);
  ask(q, function(data){
    if (data.length || parts.length < 2) { cb(data, {}); return; }
    ask(parts.slice(1).join(', '), function(retry){
      cb(retry, retry.length ? {strippedPrefix: parts[0]} : {});
    });
  });
}
function nominatimReverse(lat, lon, lang, cb){
  var url = '/search/geocode/reverse?lat=' + lat + '&lon=' + lon;
  if (lang) url += '&lang=' + encodeURIComponent(lang);
  fetch(url).then(function(r){ return r.json(); }).then(function(item){
    cb(item || null);
  }).catch(function(){ cb(null); });
}
// Re-fetches Nominatim's reverse endpoint (via nominatimReverse above) for
// (lat, lon) in the country's own language and hands the resulting
// `address` object to cb (null on failure). Callers use this as a second
// pass once an initial search/reverse call (in whatever language) has
// revealed the country_code, so town/country/region end up in the local
// language instead of whatever language the first, necessarily-blind call
// happened to use.
function nominatimRefine(lat, lon, countryCode, cb){
  nominatimReverse(lat, lon, nominatimLang(countryCode), function(item){
    cb((item && item.address) || null);
  });
}
// renderMiniCalendar draws a small multi-month event calendar into
// #containerId: one grid per month in `months` (array of Date, each the
// 1st of a month to render), days with an event highlighted and linking
// to /events/{id}. calEvents is the [{id,t,s,e}] shape produced by the
// festivalCalendarJSON template func. Shared by /festivals (fixed
// calendar year, its own month list) and /org/{slug} (#1161, a rolling
// window of upcoming months) so this rendering logic isn't duplicated.
function renderMiniCalendar(containerId, calEvents, months, opts){
  opts=opts||{};
  var cal=document.getElementById(containerId);
  if(!cal) return;
  if(!calEvents.length || !months.length){
    cal.hidden=true;
    if(opts.wrapId){var w=document.getElementById(opts.wrapId); if(w) w.hidden=true;}
    return;
  }
  var lang=document.documentElement.lang||'de';
  var fmtMonth=new Intl.DateTimeFormat(lang,{month:'long',year:'numeric'});
  var fmtWd=new Intl.DateTimeFormat(lang,{weekday:'narrow'});
  function dayKey(d){return d.getFullYear()+'-'+(d.getMonth()+1)+'-'+d.getDate();}
  // Map every day covered by an event (start..end) to the event shown there.
  var dayMap={};
  var titleById={};
  calEvents.forEach(function(ev){
    var start=new Date(ev.s);
    var end=ev.e?new Date(ev.e):start;
    var d=new Date(start.getFullYear(),start.getMonth(),start.getDate());
    var last=new Date(end.getFullYear(),end.getMonth(),end.getDate());
    while(d<=last){dayMap[dayKey(d)]=ev;d.setDate(d.getDate()+1);}
  });
  var html='';
  months.forEach(function(monthDate){
    var year=monthDate.getFullYear(), month=monthDate.getMonth();
    html+='<div class="fest-month"><div class="fest-month-title">'+fmtMonth.format(monthDate)+'</div>';
    html+='<table class="fest-month-grid"><thead><tr>';
    var wd0=new Date(year,month,1);
    var dow0=wd0.getDay();
    wd0.setDate(wd0.getDate()-(dow0===0?6:dow0-1));
    for(var i=0;i<7;i++){var hd=new Date(wd0);hd.setDate(hd.getDate()+i);html+='<th>'+fmtWd.format(hd)+'</th>';}
    html+='</tr></thead><tbody>';
    var cur=new Date(wd0);
    for(var row=0;row<6;row++){
      html+='<tr>';
      for(var col=0;col<7;col++){
        var inMonth=cur.getMonth()===month;
        var ev=dayMap[dayKey(cur)];
        if(ev){
          // The title is set through the DOM after the markup is inserted (below),
          // so event text never goes into innerHTML (CodeQL js/xss-through-dom).
          titleById[ev.id]=ev.t;
          html+='<td class="fest-day" data-id="'+ev.id+'" data-href="/events/'+ev.id+'">'+cur.getDate()+'</td>';
        } else {
          html+='<td class="'+(inMonth?'':'fest-other')+'">'+cur.getDate()+'</td>';
        }
        cur.setDate(cur.getDate()+1);
      }
      html+='</tr>';
      if(row>=3 && cur.getMonth()!==month) break;
    }
    html+='</tbody></table></div>';
  });
  cal.innerHTML=html;
  cal.hidden=false;
  cal.querySelectorAll('td.fest-day').forEach(function(td){
    td.title=titleById[td.getAttribute('data-id')]||'';
  });
  cal.querySelectorAll('td.fest-day').forEach(function(td){
    td.addEventListener('click',function(){window.location.href=this.dataset.href;});
  });
  if(opts.carousel){
    cal.classList.add('fest-carousel');
    var prevBtn=opts.prevId&&document.getElementById(opts.prevId);
    var nextBtn=opts.nextId&&document.getElementById(opts.nextId);
    function step(){
      var m=cal.querySelector('.fest-month');
      return m?m.getBoundingClientRect().width+16:cal.clientWidth;
    }
    function updateBtns(){
      if(prevBtn) prevBtn.disabled=cal.scrollLeft<=2;
      if(nextBtn) nextBtn.disabled=cal.scrollLeft+cal.clientWidth>=cal.scrollWidth-2;
    }
    if(opts.startMonth){
      var startIdx=-1;
      for(var mi=0;mi<months.length;mi++){
        if(months[mi].getMonth()+1===opts.startMonth){startIdx=mi;break;}
      }
      if(startIdx>0){
        var prevBehavior=cal.style.scrollBehavior;
        cal.style.scrollBehavior='auto';
        cal.scrollLeft=startIdx*step();
        cal.style.scrollBehavior=prevBehavior;
      }
    }
    if(prevBtn) prevBtn.onclick=function(){cal.scrollBy({left:-step(),behavior:'smooth'});};
    if(nextBtn) nextBtn.onclick=function(){cal.scrollBy({left:step(),behavior:'smooth'});};
    // #1519: /festivals re-renders this same container on every carousel<->grid
    // breakpoint crossing (innerHTML only, the container node itself persists),
    // so guard against rebinding scroll/touch listeners on every re-render —
    // prevBtn/nextBtn.onclick above is a plain assignment and safe to redo,
    // but addEventListener would otherwise stack a duplicate handler per call.
    if(!cal._miniCalCarouselBound){
      cal.addEventListener('scroll',updateBtns);
      // #1171: touch swipe, same threshold/pattern as the homepage week-table
      // swipe (index.html). overflow-x is hidden (paging is arrow/swipe-only,
      // no visible scrollbar drag), so without this a touch drag would do
      // nothing on mobile — the arrows alone aren't the "swipe like the main
      // page" the carousel was expected to support.
      var sx=null;
      cal.addEventListener('touchstart',function(e){sx=e.touches[0].clientX;},{passive:true});
      cal.addEventListener('touchend',function(e){
        if(sx===null)return;
        var dx=e.changedTouches[0].clientX-sx; sx=null;
        if(Math.abs(dx)>50) cal.scrollBy({left:dx<0?step():-step(),behavior:'smooth'});
      },{passive:true});
      cal._miniCalCarouselBound=true;
    }
    updateBtns();
  } else {
    cal.classList.remove('fest-carousel');
  }
}
function attachTileLayer(map){
  // #1169: light and dark now share one tile layer (CSS filter handles the
  // dark styling), so there's no swap-on-theme-change to observe any more.
  makeTileLayer().addTo(map);
  fixDefaultMarkerIcon();
}
// detabifyHiddenMap: for a decorative map whose container is aria-hidden
// (index.html/search.html — #1299), keeps every focusable descendant out of
// the tab order. zoomControl:false and each marker's own keyboard:false
// cover the zoom buttons and individual event markers, but miss three more
// things axe's aria-hidden-focus rule still catches: the container div
// itself (Leaflet's keyboard-pan handler sets its tabindex, silenced by the
// map's own keyboard:false option — set that too, this only mops up
// anything still marked focusable), the tile layer's attribution links, and
// — the reason this can't be a one-time sweep — marker-cluster icons, which
// Leaflet.markercluster creates and destroys on the fly as the view is
// panned/zoomed and which have no keyboard:false option of their own to
// pass through addLayer(). A MutationObserver catches those as they appear.
function detabifyHiddenMap(map){
  // '[tabindex]' alone only matches elements carrying an *explicit*
  // tabindex attribute (the container div, cluster icons — Leaflet sets
  // those via JS) — it misses natively-focusable elements that never had
  // one, like the attribution control's plain <a href> links. Match both.
  var FOCUSABLE = 'a[href],area[href],button,input,select,textarea,iframe,[tabindex]';
  function detab(el){
    if (el.nodeType !== 1) return;
    if (el.matches && el.matches(FOCUSABLE) && el.tabIndex >= 0) el.tabIndex = -1;
    if (el.querySelectorAll) el.querySelectorAll(FOCUSABLE).forEach(function(child){
      if (child.tabIndex >= 0) child.tabIndex = -1;
    });
  }
  var container = map.getContainer();
  detab(container);
  new MutationObserver(function(muts){
    muts.forEach(function(m){
      if (m.type === 'attributes') { detab(m.target); return; }
      m.addedNodes.forEach(detab);
    });
  }).observe(container, {childList:true, subtree:true, attributes:true, attributeFilter:['tabindex']});
}
function fixDefaultMarkerIcon(){
  // #1221: plain L.marker(...) calls (no explicit icon:) use Leaflet's
  // built-in default pin, whose image path Leaflet detects at runtime by
  // reading a `.leaflet-default-icon-path` background-image rule out of
  // leaflet.css — and caches whatever it finds (even nothing) for the rest
  // of the page's life. dansalLeafletCss() loads that CSS non-blocking
  // (media="print" flipped to "all" on load), so a page that creates its
  // first default marker synchronously right after calling it — before the
  // stylesheet has actually applied — gets a failed detection and every
  // default marker on the page renders as a broken-image glyph instead of
  // a pin. Set the URLs explicitly once so there's nothing to race.
  //
  // IconDefault._getIconUrl() (Leaflet's internal, not overridden here)
  // computes `(this.options.imagePath || IconDefault.imagePath) +
  // <name>Url`, i.e. it PREPENDS a separately-detected `imagePath` in
  // front of options.iconUrl -- mergeOptions() below only sets the latter.
  // Left alone, imagePath detection still runs (and, since
  // dansalLeafletCss() has already inserted the <link href=".../
  // leaflet.css"> element by the time a marker is created, its
  // querySelector('link[href$="leaflet.css"]') fallback finds it even
  // while media="print"), so the absolute URLs below would get a bogus
  // prefix concatenated onto them. Pin imagePath to '' too so detection
  // is skipped and the absolute URLs are used as-is.
  if(fixDefaultMarkerIcon.done) return;
  fixDefaultMarkerIcon.done=true;
  L.Icon.Default.imagePath='';
  L.Icon.Default.mergeOptions({
    iconUrl:'/static/leaflet/images/marker-icon.png',
    iconRetinaUrl:'/static/leaflet/images/marker-icon-2x.png',
    shadowUrl:'/static/leaflet/images/marker-shadow.png'
  });
}
function dansalLeafletCss(cluster){
  // Pagespeed's "avoid chaining critical requests" audit: Leaflet's
  // stylesheets used to be render-blocking <link>s in <head>, putting ~4KB of
  // CDN CSS on the critical path of every map page and delaying first paint.
  // Load them non-blocking instead — fetch with media="print", flip to "all"
  // once loaded. CSP blocks an inline onload= on the <link>, so the flip
  // happens here via the property (which the nonce'd script may set).
  if(dansalLeafletCss.done) return;
  dansalLeafletCss.done=true;
  var sheets=['/static/leaflet/leaflet.css'];
  if(cluster) sheets.push('/static/leaflet.markercluster/MarkerCluster.Default.css');
  sheets.forEach(function(href){
    var l=document.createElement('link');
    l.rel='stylesheet';l.href=href;l.media='print';
    l.onload=function(){l.media='all';};
    document.head.appendChild(l);
  });
}
// dansalLoadLeaflet (#1613): loads Leaflet (and optionally markercluster) from
// a script that the page does not have to block on. Returns one cached Promise.
// Scripts created by a nonce'd script are trusted under strict-dynamic, so no
// nonce is needed on the injected tags.
// dansalTabBarKeyboard (#1667): hides a phone tab bar while the on-screen
// keyboard covers it, and shows it again when the keyboard closes. The keyboard
// counts as open while a text field has focus and the visual viewport has
// shrunk well below the window. Without visualViewport, focus alone decides.
function dansalTabBarKeyboard(bar){
  var vv=window.visualViewport;
  function textFocused(){
    var a=document.activeElement;
    return !!(a && a.matches && a.matches('input:not([type=checkbox]):not([type=radio]):not([type=file]),select,textarea'));
  }
  function update(){
    bar.classList.toggle('mtabs-away', textFocused() && (!vv || vv.height < window.innerHeight*0.75));
  }
  document.addEventListener('focusin', update);
  document.addEventListener('focusout', function(){ setTimeout(update, 0); });
  if(vv) vv.addEventListener('resize', update);
}
// dansalScrollTo (#1667): scrolls so that the top of el is in view.
function dansalScrollTo(el){
  if(!el) return;
  window.scrollTo(0, el.getBoundingClientRect().top + window.pageYOffset - 8);
}
// dansalMtabs (#1671-#1675): phone tab bar for the detail pages. root holds the
// bar (.mtabs with .mtab buttons) and the panels (.mtab-panel[data-tab]). On
// desktop the bar is hidden and no panel gets data-off, so every panel shows.
// Returns show(tab, byUser); onShow(tab) runs after each switch.
function dansalMtabs(root, onShow){
  var bar=root.querySelector('.mtabs');
  if(!bar) return null;
  var btns=Array.prototype.slice.call(bar.querySelectorAll('.mtab'));
  var panels=Array.prototype.slice.call(root.querySelectorAll('.mtab-panel'));
  var emptyMsg=root.querySelector('.mtab-empty');
  document.body.classList.add('has-mtabs');
  function show(tab, byUser){
    root.setAttribute('data-mtab', tab);
    var empty=false;
    btns.forEach(function(b){
      var on=b.dataset.tab===tab;
      b.setAttribute('aria-selected', on?'true':'false');
      b.tabIndex=on?0:-1;
      if(on) empty=b.classList.contains('is-empty');
    });
    panels.forEach(function(p){ p.toggleAttribute('data-off', p.dataset.tab!==tab); });
    if(emptyMsg) emptyMsg.toggleAttribute('data-show', empty);
    if(onShow) onShow(tab);
    if(byUser) dansalScrollTo(root);
  }
  btns.forEach(function(b){
    b.addEventListener('click', function(){ show(this.dataset.tab, true); });
    b.addEventListener('keydown', function(e){
      var i=btns.indexOf(this), j=-1;
      if(e.key==='ArrowRight') j=(i+1)%btns.length;
      else if(e.key==='ArrowLeft') j=(i+btns.length-1)%btns.length;
      else if(e.key==='Home') j=0;
      else if(e.key==='End') j=btns.length-1;
      if(j<0) return;
      e.preventDefault();
      btns[j].focus();
      show(btns[j].dataset.tab, true);
    });
  });
  dansalTabBarKeyboard(bar);
  show(root.getAttribute('data-mtab') || btns[0].dataset.tab, false);
  return show;
}
// dansalSwipe (#1671): calls onLeft or onRight for a mostly horizontal swipe on el.
function dansalSwipe(el, onLeft, onRight){
  var x0=0, y0=0, on=false;
  el.addEventListener('touchstart', function(e){
    if(e.touches.length!==1) return;
    on=true; x0=e.touches[0].clientX; y0=e.touches[0].clientY;
  }, {passive:true});
  el.addEventListener('touchend', function(e){
    if(!on) return;
    on=false;
    var t=e.changedTouches[0], dx=t.clientX-x0, dy=t.clientY-y0;
    if(Math.abs(dx)>50 && Math.abs(dx)>1.5*Math.abs(dy)){
      if(dx<0 && onLeft) onLeft(); else if(dx>0 && onRight) onRight();
    }
  }, {passive:true});
}
function dansalLoadLeaflet(cluster){
  if(dansalLoadLeaflet.p) return dansalLoadLeaflet.p;
  function load(src){
    return new Promise(function(resolve, reject){
      var s=document.createElement('script');
      s.src=src; s.onload=resolve; s.onerror=reject;
      document.head.appendChild(s);
    });
  }
  dansalLeafletCss(cluster);
  dansalLoadLeaflet.p=load('/static/leaflet/leaflet.js').then(function(){
    return cluster ? load('/static/leaflet.markercluster/leaflet.markercluster.js') : null;
  });
  return dansalLoadLeaflet.p;
}
(function(){
  var btn=document.getElementById('back-to-top');
  var nav=document.querySelector('nav');
  if(!btn||!nav)return;
  new IntersectionObserver(function(e){btn.classList.toggle('visible',!e[0].isIntersecting);}).observe(nav);
})();
var _langPending=null;
function handleLangChange(sel){
  var code=sel.value;
  var hasCookie=document.cookie.split(';').some(function(c){return c.trim().startsWith('dsw_lang=');});
  if(hasCookie){location.href='/lang?code='+code;return;}
  _langPending=code;
  document.getElementById('lang-consent-modal').hidden=false;
}
function langConsentAccept(){
  document.getElementById('lang-consent-modal').hidden=true;
  if(_langPending){location.href='/lang?code='+_langPending;}
}
function langConsentDeny(){
  document.getElementById('lang-consent-modal').hidden=true;
  var sel=document.getElementById('lang-select');
  if(sel){var opts=sel.options;for(var i=0;i<opts.length;i++){if(opts[i].defaultSelected){sel.selectedIndex=i;break;}}}
}

// initDateRangePicker — reusable two-click date-range calendar popup.
// opts: {btn, popup, grid, title, prev, next, from, to, placeholder, onSelect}
// onSelect(fromISO, toISO) is called when the user completes a selection.
// Same-day: first click = half-circle; second click on same date = full circle.
// dateUnusual (#1413) classifies an ISO date (YYYY-MM-DD) for the
// "are you sure about this date?" warnings: 'past' (before today), 'far'
// (more than 2 years ahead — usually a year typo) or '' (fine / empty).
// Shared by the date picker, the admin event form and the publish dialogs.
function dateUnusual(iso){
  if(!iso) return '';
  var t=new Date(), pad=function(n){return n<10?'0'+n:''+n;};
  var today=t.getFullYear()+'-'+pad(t.getMonth()+1)+'-'+pad(t.getDate());
  var far=(t.getFullYear()+2)+'-'+pad(t.getMonth()+1)+'-'+pad(t.getDate());
  return iso<today?'past':(iso>far?'far':'');
}

// confirmUnusualDate (#1413) opens the shared #unusual-date-dialog
// (base.html, logged-in users only). kind is 'past', 'far' or 'bulk'; arg
// fills the message's %s (a date, or a count for 'bulk'); okKind picks the
// confirm label ('save' or 'publish'). Calls onOk on confirm, onBack (if
// given) on "back to edit"; closing with Esc counts as back. Without the
// dialog on the page it just proceeds — the server-side flash after
// publishing (#1413) is the safety net either way.
function confirmUnusualDate(kind, arg, okKind, onOk, onBack){
  var dlg=document.getElementById('unusual-date-dialog');
  if(!dlg||typeof dlg.showModal!=='function'){ onOk(); return; }
  var key='msg'+kind.charAt(0).toUpperCase()+kind.slice(1);
  dlg.querySelector('.ud-msg').textContent=(dlg.dataset[key]||'').replace('%s',arg);
  var ok=dlg.querySelector('.ud-ok'), back=dlg.querySelector('.ud-back');
  ok.textContent=okKind==='publish'?dlg.dataset.okPublish:dlg.dataset.okSave;
  // Resolve straight from the button/Esc handlers rather than the dialog's
  // 'close' event: Chrome dispatches 'close' asynchronously in its rendering
  // steps, which don't run at all in a hidden tab (found while testing in a
  // background tab) — the dialog closed but neither callback ever fired.
  var done=false;
  function finish(confirmed){
    if(done) return;
    done=true;
    dlg.close();
    if(confirmed) onOk(); else if(onBack) onBack();
  }
  ok.onclick=function(){ finish(true); };
  back.onclick=function(){ finish(false); };
  dlg.oncancel=function(e){ e.preventDefault(); finish(false); }; // Esc
  dlg.showModal();
  back.focus();
}

// Publish forms opt in with data-date-check="YYYY-MM-DD" (single event) —
// or a submit button with data-date-check-bulk="<checkbox selector>", whose
// checked boxes carry data-date — and get the unusual-date dialog before
// submitting (#1413). Runs after data-confirm (click) since this is submit.
document.addEventListener('submit', function(e){
  var form=e.target;
  if(!(form instanceof HTMLFormElement)||form.dataset.dateConfirmed==='1') return;
  var sub=e.submitter, kind='', arg='';
  if(sub&&sub.hasAttribute('data-date-check-bulk')){
    var n=0;
    document.querySelectorAll(sub.getAttribute('data-date-check-bulk')).forEach(function(cb){
      if(cb.checked&&dateUnusual(cb.dataset.date)) n++;
    });
    if(n){ kind='bulk'; arg=String(n); }
  } else if(form.hasAttribute('data-date-check')){
    kind=dateUnusual(form.getAttribute('data-date-check'));
    arg=form.getAttribute('data-date-label')||form.getAttribute('data-date-check');
  }
  if(!kind) return;
  e.preventDefault();
  confirmUnusualDate(kind, arg, 'publish', function(){
    form.dataset.dateConfirmed='1';
    if(sub&&form.requestSubmit) form.requestSubmit(sub); else form.submit();
  });
}, true);

// initDateRangePicker options beyond the elements: markPast greys days
// before today (still clickable — admins may record past events), blockPast
// also makes them unclickable (public suggest form). With either, the
// button turns amber while the chosen start date is unusual (#1413).
function initDateRangePicker(opts){
  var pendingStart=null;
  var from=opts.from||'', to=opts.to||'';
  var calYear, calMonth;
  var checkPast=opts.markPast||opts.blockPast;
  function pad(n){return n<10?'0'+n:''+n;}
  function iso(d){return d.getFullYear()+'-'+pad(d.getMonth()+1)+'-'+pad(d.getDate());}
  function updateBtn(){
    opts.btn.textContent = from&&to ? (from===to ? from : from+' → '+to) : (opts.placeholder||'📅');
    if(checkPast) opts.btn.classList.toggle('dpr-btn-warn', dateUnusual(from)!=='');
  }
  updateBtn();
  function render(){
    var lang=document.documentElement.lang||'de';
    var fmtMY=new Intl.DateTimeFormat(lang,{month:'long',year:'numeric'});
    var fmtWd=new Intl.DateTimeFormat(lang,{weekday:'narrow'});
    opts.title.textContent=fmtMY.format(new Date(calYear,calMonth,1));
    var cur=new Date(calYear,calMonth,1);
    var dow=cur.getDay(); cur.setDate(cur.getDate()-(dow===0?6:dow-1));
    var today=iso(new Date());
    var selS=pendingStart||from, selE=pendingStart?pendingStart:to;
    if(selS>selE){var t=selS;selS=selE;selE=t;}
    var h='<thead><tr>';
    for(var i=0;i<7;i++){var hd=new Date(cur);hd.setDate(hd.getDate()+i);h+='<th>'+fmtWd.format(hd)+'</th>';}
    h+='</tr></thead><tbody>';
    for(var row=0;row<6;row++){
      h+='<tr>';
      for(var col=0;col<7;col++){
        var d=iso(cur);
        var cls=cur.getMonth()!==calMonth?'dpr-other':'';
        if(!pendingStart&&d>=selS&&d<=selE) cls+=' dpr-in-range';
        if(d===selS) cls+=' dpr-range-start';
        if(d===selE&&!pendingStart) cls+=' dpr-range-end';
        if(pendingStart&&d===pendingStart) cls+=' dpr-range-start dpr-range-end';
        if(d===today) cls+=' dpr-today';
        if(checkPast&&d<today) cls+=opts.blockPast?' dpr-past dpr-blocked':' dpr-past';
        h+='<td class="'+cls.trim()+'" data-iso="'+d+'">'+cur.getDate()+'</td>';
        cur.setDate(cur.getDate()+1);
      }
      h+='</tr>';
      if(row>=3&&cur.getMonth()!==calMonth) break;
    }
    h+='</tbody>';
    opts.grid.innerHTML=h;
    opts.grid.querySelectorAll('td').forEach(function(td){
      td.addEventListener('click',function(){
        var d=this.dataset.iso;
        if(this.classList.contains('dpr-blocked')) return;
        if(!pendingStart){
          pendingStart=d;
          setTimeout(render,0);
        } else {
          var s=pendingStart,e=d;
          if(e<s){var t=s;s=e;e=t;}
          pendingStart=null;
          from=s; to=e;
          updateBtn();
          opts.popup.hidden=true;
          opts.onSelect(from,to);
        }
      });
    });
  }
  opts.btn.addEventListener('click',function(){
    var d=new Date((from||iso(new Date()))+'T00:00:00');
    calYear=d.getFullYear(); calMonth=d.getMonth();
    pendingStart=null;
    opts.popup.hidden=!opts.popup.hidden;
    if(!opts.popup.hidden) render();
  });
  opts.prev.addEventListener('click',function(e){
    e.preventDefault();
    e.stopPropagation();
    if(--calMonth<0){calMonth=11;calYear--;}
    render();
  });
  opts.next.addEventListener('click',function(e){
    e.preventDefault();
    e.stopPropagation();
    if(++calMonth>11){calMonth=0;calYear++;}
    render();
  });
  document.addEventListener('click',function(e){
    if(!opts.popup.hidden&&!opts.popup.contains(e.target)&&e.target!==opts.btn)
      opts.popup.hidden=true;
  });
  // Adopt a programmatically-set range (e.g. wizard prefill) so the button
  // label, calendar default month and range highlight all stay in sync.
  opts.setRange = function(f, t) {
    from = f || '';
    to = t || from;
    updateBtn();
  };
  return opts;
}

// --- Markdown mini-editor + image drop-zone (#1322) ---
// Extracted from 5 admin edit forms (event/instructor/location/musician/org)
// that each carried a byte-identical copy of this widget as inline <script>.
// Page-specific bits (which textarea/field ids to bind) stay in each
// template as a one-line call, same as _bindMdAutosize('description') below.

// removeImage: data-fn target for the "remove image" button (admin_*_edit.html).
function removeImage(url){var f=document.createElement('form');f.method='post';f.action=url;document.body.appendChild(f);f.submit();}

// _bindMdPaste: pasting rich-text (e.g. from Word/Google Docs) into a
// markdown textarea converts it to markdown instead of dumping raw HTML/
// plain text, with a brief hint shown confirming the conversion happened.
function _bindMdPaste(taId,hintId){
  var ta=document.getElementById(taId),hint=document.getElementById(hintId);
  if(!ta||!hint) return;
  function hasRich(h){return /<(b|strong|i|em|h[1-6]|ul|ol|blockquote|code|pre|hr)[\s>\/]/i.test(h)||/<a\s/i.test(h);}
  function n2m(n){
    if(n.nodeType===3) return n.textContent;
    if(n.nodeType!==1) return '';
    var tag=n.tagName.toLowerCase(),inner=Array.from(n.childNodes).map(n2m).join('');
    switch(tag){
      case 'b':case 'strong':{var t=inner.trim();return t?'**'+t+'**':'';}
      case 'i':case 'em':{var t=inner.trim();return t?'*'+t+'*':'';}
      case 'h1':return '\n\n# '+inner.trim()+'\n\n';
      case 'h2':return '\n\n## '+inner.trim()+'\n\n';
      case 'h3':return '\n\n### '+inner.trim()+'\n\n';
      case 'h4':case 'h5':case 'h6':return '\n\n#### '+inner.trim()+'\n\n';
      case 'p':return '\n\n'+inner.trim()+'\n\n';
      case 'br':return '\n';
      case 'ul':case 'ol':return '\n'+inner+'\n';
      case 'li':{var ol=n.parentElement&&n.parentElement.tagName.toLowerCase()==='ol';if(ol){var idx=Array.from(n.parentElement.children).indexOf(n)+1;return idx+'. '+inner.trim()+'\n';}return '- '+inner.trim()+'\n';}
      case 'a':{var href=n.getAttribute('href')||'',txt=inner.trim();return href&&href!==txt?'['+txt+']('+href+')':txt;}
      case 'blockquote':return '\n\n'+inner.trim().split('\n').map(function(l){return '> '+l;}).join('\n')+'\n\n';
      case 'code':return '`'+inner+'`';
      case 'pre':return '\n\n```\n'+n.textContent.trim()+'\n```\n\n';
      case 'hr':return '\n\n---\n\n';
      case 'head':case 'style':case 'script':case 'meta':return '';
      default:return inner;
    }
  }
  function toMd(html){var d=new DOMParser().parseFromString(html,'text/html');return n2m(d.body).replace(/[ \t]+\n/g,'\n').replace(/\n{3,}/g,'\n\n').trim();}
  var timer;
  ta.addEventListener('paste',function(e){
    var html=e.clipboardData&&e.clipboardData.getData('text/html');
    if(!html||!hasRich(html)) return;
    e.preventDefault();
    var md=toMd(html),s=ta.selectionStart,end=ta.selectionEnd,v=ta.value;
    ta.value=v.slice(0,s)+md+v.slice(end);
    ta.selectionStart=ta.selectionEnd=s+md.length;
    ta.dispatchEvent(new Event('input'));
    hint.hidden=false;
    clearTimeout(timer);
    timer=setTimeout(function(){hint.hidden=true;},5000);
    if(typeof _markDirty==='function') _markDirty();
  });
}
// _mdToHtml: the small markdown-to-HTML renderer backing every admin edit
// form's live preview pane. Deliberately not a full CommonMark
// implementation — just the subset _bindMdPaste's n2m() above produces
// (bold/italic/code/links/headings/lists/blockquotes/hr/fenced code).
function _mdToHtml(md){
  if(!md) return '';
  function esc(s){return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');}
  function inline(s){
    s=esc(s);
    s=s.replace(/`([^`]+)`/g,'<code>$1</code>');
    s=s.replace(/\*\*\*(.+?)\*\*\*/g,'<strong><em>$1</em></strong>');
    s=s.replace(/\*\*(.+?)\*\*/g,'<strong>$1</strong>');
    s=s.replace(/\*(.+?)\*/g,'<em>$1</em>');
    s=s.replace(/_(.+?)_/g,'<em>$1</em>');
    s=s.replace(/\[([^\]]+)\]\(([^)]+)\)/g,function(_,txt,url){
      url=url.trim();
      if(!/^(https?:|mailto:|tel:|\/|#)/i.test(url)) url='#';
      return '<a href="'+url.replace(/"/g,'&quot;')+'" target="_blank" rel="noopener">'+txt+'</a>';
    });
    return s;
  }
  var lines=md.split('\n'),out=[],inUl=false,inOl=false,inPre=false,preBuf=[];
  for(var i=0;i<lines.length;i++){
    var l=lines[i];
    if(/^```/.test(l)){
      if(inPre){out.push('<pre><code>'+esc(preBuf.join('\n'))+'</code></pre>');preBuf=[];inPre=false;}
      else{if(inUl){out.push('</ul>');inUl=false;}if(inOl){out.push('</ol>');inOl=false;}inPre=true;}
      continue;
    }
    if(inPre){preBuf.push(l);continue;}
    var hm=l.match(/^(#{1,6}) (.*)/);
    if(hm){if(inUl){out.push('</ul>');inUl=false;}if(inOl){out.push('</ol>');inOl=false;}out.push('<h'+hm[1].length+'>'+inline(hm[2])+'</h'+hm[1].length+'>');continue;}
    if(/^>\s/.test(l)){if(inUl){out.push('</ul>');inUl=false;}if(inOl){out.push('</ol>');inOl=false;}out.push('<blockquote>'+inline(l.replace(/^>\s*/,''))+'</blockquote>');continue;}
    if(/^-{3,}$/.test(l.trim())){if(inUl){out.push('</ul>');inUl=false;}if(inOl){out.push('</ol>');inOl=false;}out.push('<hr>');continue;}
    var ulm=l.match(/^[*-] (.*)/);
    if(ulm){if(inOl){out.push('</ol>');inOl=false;}if(!inUl){out.push('<ul>');inUl=true;}out.push('<li>'+inline(ulm[1])+'</li>');continue;}
    var olm=l.match(/^\d+\. (.*)/);
    if(olm){if(inUl){out.push('</ul>');inUl=false;}if(!inOl){out.push('<ol>');inOl=true;}out.push('<li>'+inline(olm[1])+'</li>');continue;}
    if(inUl){out.push('</ul>');inUl=false;}if(inOl){out.push('</ol>');inOl=false;}
    if(!l.trim()){out.push('<p class="md-br"></p>');continue;}
    out.push('<p>'+inline(l)+'</p>');
  }
  if(inUl)out.push('</ul>');if(inOl)out.push('</ol>');
  if(inPre)out.push('<pre><code>'+esc(preBuf.join('\n'))+'</code></pre>');
  return out.join('');
}
// _bindMdAutosize: wraps a markdown textarea with a live preview pane and an
// expand/collapse toggle, growing the textarea to fit its content.
function _bindMdAutosize(taId){
  var ta=document.getElementById(taId);
  if(!ta) return;
  var ch=ta.offsetHeight||(parseInt(ta.getAttribute('rows')||'4')*22+14);
  var wrap=document.createElement('div');
  wrap.className='md-ta-wrap';
  ta.parentNode.insertBefore(wrap,ta);
  wrap.appendChild(ta);
  var left=document.createElement('div');
  left.className='md-ta-left';
  wrap.insertBefore(left,ta);
  left.appendChild(ta);
  var btn=document.createElement('button');
  btn.type='button';btn.className='md-ta-toggle';btn.title='Collapse';btn.textContent='▲';
  left.appendChild(btn);
  var prev=document.createElement('div');
  prev.className='md-preview';
  wrap.appendChild(prev);
  var descRow=wrap.closest&&wrap.closest('.desc-row');
  if(descRow){var mh=descRow.querySelector('.md-help');if(mh)mh.hidden=true;}
  ta.style.overflow='hidden';ta.style.resize='none';
  var open=true;
  function renderPrev(){prev.innerHTML=_mdToHtml(ta.value)||'<span class="md-preview-empty">Preview</span>';}
  function doExpand(){open=true;ta.style.height='0';ta.style.height=Math.max(ch,ta.scrollHeight)+'px';ta.style.overflow='hidden';btn.textContent='▲';btn.title='Collapse';}
  function doCollapse(){open=false;ta.style.height=ch+'px';ta.style.overflow='auto';btn.textContent='▼';btn.title='Expand';}
  renderPrev();
  if(ta.offsetHeight>0){doExpand();}
  else{var ro=new ResizeObserver(function(e){if(e[0].contentRect.height>0){doExpand();ro.disconnect();}});ro.observe(ta);}
  ta.addEventListener('input',function(){if(open)doExpand();renderPrev();});
  btn.addEventListener('click',function(){if(open)doCollapse();else doExpand();});
}
// initImageDropZone: drag&drop + click-to-pick preview for the admin edit
// forms' image upload field. Self-guarding (no-ops when the drop-zone
// elements aren't on the page, e.g. admin_event_form.html's template mode),
// so it's called unconditionally below instead of requiring each template
// to call it itself.
function initImageDropZone(){
  var dz=document.getElementById('drop-zone');
  var inp=document.getElementById('image');
  var prev=document.getElementById('image-preview');
  var lbl=document.getElementById('drop-label');
  if(!dz||!inp||!prev||!lbl) return;
  function showPreview(file){
    var url=URL.createObjectURL(file);
    prev.src=url;prev.style.display='block';
    lbl.style.display='none';
  }
  inp.addEventListener('change',function(){if(this.files[0])showPreview(this.files[0]);});
  dz.addEventListener('dragover',function(e){e.preventDefault();dz.classList.add('drag-over');});
  dz.addEventListener('dragleave',function(){dz.classList.remove('drag-over');});
  dz.addEventListener('drop',function(e){
    e.preventDefault();dz.classList.remove('drag-over');
    var f=e.dataTransfer.files[0];
    if(f&&f.type.startsWith('image/')){
      var dt=new DataTransfer();dt.items.add(f);inp.files=dt.files;
      showPreview(f);
    }
  });
}
initImageDropZone();

// initLongPressMultiSelect: shared long-press-on-touch machinery for the
// admin list pages' mobile bulk-select drawer (admin_events.html,
// admin_fetchurls.html, admin_locations.html, admin_series_edit.html).
// Each page's own enterMultiSelect/exitMultiSelect/updateCount stay
// page-specific (different checkbox classes, different optional toolbar
// elements) -- only the touch-timing/tolerance boilerplate was actually
// identical across all four (#1322). Returns an object whose
// suppressClick flag a caller can check in its own tbody 'click' handler
// to ignore the synthetic click that follows a long-press on some
// browsers (admin_series_edit.html's tbody does this).
function initLongPressMultiSelect(tbody, rowSelector, onLongPress){
  if(!tbody) return null;
  var LONG_PRESS_MS=500, MOVE_TOLERANCE=10;
  var pressTimer=null, startX=0, startY=0, pressRow=null;
  var api={suppressClick:false};
  function clearPressTimer(){ if(pressTimer){ clearTimeout(pressTimer); pressTimer=null; } pressRow=null; }
  tbody.addEventListener('touchstart',function(e){
    if(!window.matchMedia('(max-width:640px)').matches) return;
    var row=e.target.closest(rowSelector);
    if(!row) return;
    pressRow=row;
    startX=e.touches[0].clientX; startY=e.touches[0].clientY;
    pressTimer=setTimeout(function(){ onLongPress(pressRow); api.suppressClick=true; clearPressTimer(); },LONG_PRESS_MS);
  },{passive:true});
  tbody.addEventListener('touchmove',function(e){
    if(!pressTimer) return;
    var dx=Math.abs(e.touches[0].clientX-startX), dy=Math.abs(e.touches[0].clientY-startY);
    if(dx>MOVE_TOLERANCE||dy>MOVE_TOLERANCE) clearPressTimer();
  },{passive:true});
  tbody.addEventListener('touchend',clearPressTimer);
  tbody.addEventListener('touchcancel',clearPressTimer);
  return api;
}

function openHelpModal(){document.getElementById('hm-backdrop').hidden=false;}
function closeHelpModal(){document.getElementById('hm-backdrop').hidden=true;}
document.addEventListener('keydown',function(e){
  if(e.key==='Escape'){closeHelpModal();return;}
  if(e.key==='?'&&!e.ctrlKey&&!e.metaKey&&!e.altKey&&!['INPUT','TEXTAREA','SELECT'].includes(document.activeElement.tagName)){
    var el=document.getElementById('hm-backdrop');
    el.hidden?openHelpModal():closeHelpModal();
  }
});

// ─── media links editor (#1360/#1361) ────────────────────────────────────────
// Repeatable rows for an owner's external link list (template "media-links-
// editor" in base.html). Rows post as parallel media_kind/media_title/
// media_url fields; the server keeps their DOM order as the display order.
function mediaEditorChanged(ed){
  if(typeof _markDirty==='function') _markDirty();
  // Adding/removing/moving rows raises no input event of its own; emit one so
  // section-completeness dots and any form-level listeners see the change.
  if(ed){
    ed.dispatchEvent(new Event('input',{bubbles:true}));
    ed.dispatchEvent(new Event('change',{bubbles:true}));
  }
}
function mediaAddRow(btn){
  var ed=btn.closest('.media-editor');
  var rows=ed.querySelector('.media-rows');
  var max=parseInt(ed.getAttribute('data-max'),10)||20;
  if(rows.children.length>=max) return;
  var row=ed.querySelector('template.media-row-tpl').content.firstElementChild.cloneNode(true);
  rows.appendChild(row);
  var url=row.querySelector('input[type=url]');
  if(url) url.focus();
  mediaEditorChanged(ed);
}
function mediaRemoveRow(btn){
  var row=btn.closest('.media-row');
  if(!row) return;
  var ed=row.closest('.media-editor');
  row.remove();
  mediaEditorChanged(ed);
}
function mediaMoveRow(btn,dir){
  var row=btn.closest('.media-row');
  if(!row) return;
  var sib=dir<0?row.previousElementSibling:row.nextElementSibling;
  if(!sib) return;
  var ed=row.closest('.media-editor');
  if(dir<0) row.parentNode.insertBefore(row,sib); else row.parentNode.insertBefore(sib,row);
  mediaEditorChanged(ed);
}

// ─── watchlist (#1554) ───────────────────────────────────────────────────────
// The visitor's watchlist lives in this browser only: one localStorage key
// holding {v:1, ids:[…]}. The server stores nothing for it; the watchlist page
// resolves the ids through POST /watchlist/events and drops ids it reports missing.
var WL_KEY='dansal_watchlist', WL_MAX=200;

// #1605: the offline snapshot. The watchlist page saves the entries of each
// successful lookup here. When a lookup fails (no connection), the page shows
// them from the snapshot. Removing an entry also removes it from the snapshot.
var WL_SNAP_KEY='dansal_watchlist_snapshot';
function watchlistSnapshotRead(){
  try{
    var o=JSON.parse(localStorage.getItem(WL_SNAP_KEY)||'null');
    if(o&&o.v===1&&Array.isArray(o.items)) return o;
  }catch(e){}
  return null;
}
function watchlistSnapshotWrite(snap){
  try{ localStorage.setItem(WL_SNAP_KEY,JSON.stringify(snap)); }catch(e){}
}

function watchlistRead(){
  try{
    var o=JSON.parse(localStorage.getItem(WL_KEY)||'null');
    if(o&&o.v===1&&Array.isArray(o.ids)){
      return o.ids.filter(function(x){return Number.isInteger(x)&&x>0;});
    }
  }catch(e){}
  return [];
}

function watchlistHas(id){ return watchlistRead().indexOf(id)>=0; }

function watchlistWrite(ids){
  ids=ids.slice(0,WL_MAX);
  try{ localStorage.setItem(WL_KEY,JSON.stringify({v:1,ids:ids})); }catch(e){}
  var snap=watchlistSnapshotRead();
  if(snap){
    snap.items=snap.items.filter(function(it){return ids.indexOf(it.id)>=0;});
    watchlistSnapshotWrite(snap);
  }
  watchlistUI();
  // The calendar and map listen for this to show watched events (#1554).
  document.dispatchEvent(new CustomEvent('watchlistchange'));
}

window.watchlistToggle=function(id){
  id=parseInt(id,10);
  var ids=watchlistRead();
  var i=ids.indexOf(id);
  if(i>=0){ ids.splice(i,1); } else { ids.unshift(id); }
  watchlistWrite(ids);
  if(i>=0 && document.getElementById('wl-list')) watchlistRender();
};

// watchlistUI keeps every star in step with storage: the header star is filled
// when the list has entries, and each event star reflects its own event.
function watchlistUI(){
  var ids=watchlistRead();
  var hdr=document.getElementById('wl-header-star');
  if(hdr) hdr.textContent=ids.length?'★':'☆';
  var stars=document.querySelectorAll('[data-wl-star]');
  for(var s=0;s<stars.length;s++){
    var on=ids.indexOf(parseInt(stars[s].getAttribute('data-wl-star'),10))>=0;
    var glyph=stars[s].querySelector('.wl-star-glyph')||stars[s];
    glyph.textContent=on?'★':'☆';
    stars[s].setAttribute('aria-pressed',on?'true':'false');
  }
}

// watchlistRender fills the watchlist page from storage. Titles are set with
// textContent, never innerHTML.
function watchlistRender(){
  var list=document.getElementById('wl-list');
  if(!list) return;
  var empty=document.getElementById('wl-empty');
  var ids=watchlistRead();
  list.textContent='';
  if(!ids.length){ if(empty) empty.hidden=false; return; }
  if(empty) empty.hidden=true;
  fetch(list.getAttribute('data-endpoint'),{
    method:'POST', credentials:'same-origin',
    headers:{'Content-Type':'application/json'},
    body:JSON.stringify({ids:ids})
  }).then(function(r){ if(!r.ok) throw new Error('lookup '+r.status); return r.json(); })
  .then(function(data){
    var missing=data.missing||[];
    if(missing.length){
      watchlistWrite(watchlistRead().filter(function(id){return missing.indexOf(id)<0;}));
    }
    list.textContent='';
    var items=data.items||[];
    // #1605: an online visit refreshes the offline snapshot.
    watchlistSnapshotWrite({v:1,saved_at:new Date().toISOString(),items:items});
    var note=document.getElementById('wl-offline-note');
    if(note) note.hidden=true;
    items.forEach(function(it){ list.appendChild(watchlistRow(it,list)); });
    if(!watchlistRead().length && empty) empty.hidden=false;
  }).catch(function(){
    // A failed lookup keeps the stored ids. Without a connection the page shows
    // the last snapshot instead (#1605); a network or API error never drops entries.
    watchlistRenderSnapshot(list);
  });
}

// watchlistRenderSnapshot shows the stored ids from the offline snapshot (#1605).
// Ids the snapshot has no entry for were never loaded online; they are listed
// as not available offline.
function watchlistRenderSnapshot(list){
  var note=document.getElementById('wl-offline-note');
  var snap=watchlistSnapshotRead();
  var byId={};
  if(snap) snap.items.forEach(function(it){ byId[it.id]=it; });
  list.textContent='';
  watchlistRead().forEach(function(id){
    var it=byId[id]||{id:id,title:'#'+id,path:'/events/'+id,unavailable:true};
    list.appendChild(watchlistRow(it,list));
  });
  if(note){
    note.hidden=!snap;
    if(snap){
      note.textContent=(note.getAttribute('data-offline-snapshot')||'').replace('%s',new Date(snap.saved_at).toLocaleString());
    }
  }
}

function watchlistRow(it,list){
  var li=document.createElement('li');
  li.className='wl-row';
  var a=document.createElement('a');
  a.href=it.path;
  a.textContent=it.title;
  li.appendChild(a);
  var meta=document.createElement('span');
  meta.className='wl-meta';
  if(it.unavailable){
    meta.textContent=list.getAttribute('data-offline-unavailable')||'';
  } else {
    meta.textContent=[it.start_time?it.start_time.slice(0,16).replace('T',' '):'',it.location||'',it.address||''].filter(Boolean).join(' · ');
  }
  li.appendChild(meta);
  if(it.cancelled){
    var c=document.createElement('span');
    c.className='wl-cancelled';
    c.textContent=list.getAttribute('data-cancelled-label')||'';
    li.appendChild(c);
  }
  // #1605: the same navigate link as the event and location pages, plus a
  // copy-coordinates fallback for apps that do not open geo: links.
  if(typeof it.lat==='number' && typeof it.lon==='number'){
    var pos=it.lat+','+it.lon;
    var nav=document.createElement('a');
    nav.className='loc-map-link loc-map-link-nav';
    nav.href='geo:'+pos+'?q='+pos;
    nav.rel='nofollow';
    nav.title=list.getAttribute('data-navigate-hint')||'';
    nav.textContent='🧭 '+(list.getAttribute('data-navigate-label')||'');
    li.appendChild(nav);
    var cp=document.createElement('button');
    cp.type='button';
    cp.className='btn-sm-secondary wl-copy';
    cp.textContent=list.getAttribute('data-copy-label')||'';
    cp.addEventListener('click',function(){
      if(!navigator.clipboard) return;
      navigator.clipboard.writeText(pos).then(function(){
        cp.textContent=list.getAttribute('data-copied-label')||cp.textContent;
      }).catch(function(){});
    });
    li.appendChild(cp);
  }
  var btn=document.createElement('button');
  btn.type='button';
  btn.className='btn-sm-secondary wl-remove';
  btn.textContent=list.getAttribute('data-remove-label')||'×';
  btn.setAttribute('data-fn','watchlistToggle');
  btn.setAttribute('data-args','['+it.id+']');
  btn.setAttribute('data-wl-remove','1');
  li.appendChild(btn);
  return li;
}

// watchlistExport downloads the list as a JSON file. The file names the dansal
// instance it came from, so an import on another instance can refuse it.
window.watchlistExport=function(){
  var doc={format:'dansal-watchlist',version:1,instance:location.host,
           exported_at:new Date().toISOString(),ids:watchlistRead()};
  var url=URL.createObjectURL(new Blob([JSON.stringify(doc,null,2)],{type:'application/json'}));
  var a=document.createElement('a');
  a.href=url; a.download='dansal-watchlist.json';
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(function(){ URL.revokeObjectURL(url); },1000);
};

// watchlistImport merges a watchlist file into the local list (union, capped at
// WL_MAX). It checks the format, version, instance and id rules first.
window.watchlistImport=function(input){
  var file=input.files&&input.files[0];
  input.value='';
  if(!file) return;
  var list=document.getElementById('wl-list');
  var msg=document.getElementById('wl-io-msg');
  var say=function(key){ if(msg) msg.textContent=list?(list.getAttribute('data-msg-'+key)||''):''; };
  if(file.size>20000){ say('bad'); return; }
  file.text().then(function(t){
    var doc=JSON.parse(t);
    if(!doc||doc.format!=='dansal-watchlist'||doc.version!==1||!Array.isArray(doc.ids)){ throw 'bad'; }
    if(doc.instance!==location.host){ throw 'instance'; }
    var ids=doc.ids;
    for(var i=0;i<ids.length;i++){ if(!Number.isInteger(ids[i])||ids[i]<=0) throw 'bad'; }
    var have=watchlistRead();
    var merged=have.slice();
    ids.forEach(function(id){ if(merged.indexOf(id)<0) merged.push(id); });
    var capped=merged.length>WL_MAX;
    watchlistWrite(merged);
    if(capped) say('capped'); else say('ok');
    watchlistRender();
  }).catch(function(e){ say(e==='instance'?'instance':'bad'); });
};

// ─── server copy under a random key (#1554) ────────────────────────────────
// The browser keeps a UUID (identifies this browser's list on the server) and,
// once a save has created one, the random key. Saving with a key updates the
// server list; saving without one creates a new key. Loading with a key merges
// the server ids into this browser's list and keeps the key for later saves.
var WL_UUID_KEY='dansal_watchlist_uuid', WL_SKEY='dansal_watchlist_key';

function watchlistBrowserID(){
  try{
    var u=localStorage.getItem(WL_UUID_KEY);
    if(!u){
      u=(window.crypto&&crypto.randomUUID)?crypto.randomUUID():(Date.now()+'-'+Math.random().toString(36).slice(2));
      localStorage.setItem(WL_UUID_KEY,u);
    }
    return u;
  }catch(e){ return ''; }
}

function watchlistStoredKey(){
  try{ return localStorage.getItem(WL_SKEY)||''; }catch(e){ return ''; }
}

// wlMsg shows a status line from the page's data-msg-<name> attributes.
function wlMsg(name){
  var list=document.getElementById('wl-list');
  var msg=document.getElementById('wl-io-msg');
  if(msg&&list) msg.textContent=list.getAttribute('data-msg-'+name)||'';
}

function wlShowKey(key){
  var box=document.getElementById('wl-key-box');
  var out=document.getElementById('wl-key-out');
  if(out) out.textContent=key;
  if(box) box.hidden=false;
}

window.watchlistSaveServer=function(){
  var key=watchlistStoredKey();
  var hp=document.querySelector('input[name="dansal_phone2"]');
  fetch('/watchlist/form-token',{credentials:'same-origin'})
  .then(function(r){ if(!r.ok) throw 'bad'; return r.json(); })
  // The guard refuses a form token younger than a second (formguard.go), so
  // hold the save until the token is old enough.
  .then(function(t){ return new Promise(function(res){ setTimeout(function(){ res(t); },1200); }); })
  .then(function(t){
    var body=new URLSearchParams();
    body.set('_form_token',t.form_token);
    body.set('dansal_phone2',hp?hp.value:'');
    body.set('uuid',watchlistBrowserID());
    body.set('ids',watchlistRead().join(','));
    if(key) body.set('key',key);
    return fetch('/watchlist/save',{method:'POST',credentials:'same-origin',
      headers:{'Content-Type':'application/x-www-form-urlencoded'},body:body.toString()});
  })
  .then(function(r){
    if(r.status===429) throw 'rate';
    if(r.status===404) throw 'unknown';
    if(!r.ok) throw 'bad';
    return r.json();
  })
  .then(function(d){
    if(d.key){ try{ localStorage.setItem(WL_SKEY,d.key); }catch(e){} wlShowKey(d.key); }
    wlMsg('saved');
  })
  .catch(function(e){
    wlMsg(e==='rate'?'rate':e==='unknown'?'key-unknown':'server-bad');
  });
};

window.watchlistLoadServer=function(){
  var input=document.getElementById('wl-key-in');
  var key=((input&&input.value)||'').trim().toLowerCase();
  if(!/^[0-9a-f]{32}$/.test(key)){ wlMsg('key-unknown'); return; }
  fetch('/watchlist/load',{credentials:'same-origin',headers:{'X-Watchlist-Key':key}})
  .then(function(r){
    if(r.status===429) throw 'rate';
    if(r.status===404) throw 'unknown';
    if(!r.ok) throw 'bad';
    return r.json();
  })
  .then(function(d){
    var merged=watchlistRead().slice();
    (d.ids||[]).forEach(function(id){ if(merged.indexOf(id)<0) merged.push(id); });
    watchlistWrite(merged);
    try{ localStorage.setItem(WL_SKEY,key); }catch(e){}
    if(input) input.value='';
    wlMsg('loaded');
    watchlistRender();
  })
  .catch(function(e){
    wlMsg(e==='rate'?'rate':e==='unknown'?'key-unknown':'server-bad');
  });
};

document.addEventListener('DOMContentLoaded',function(){
  watchlistUI();
  watchlistRender();
  // The page may have drawn the calendar and map before this script loaded.
  document.dispatchEvent(new CustomEvent('watchlistchange'));
});
window.addEventListener('storage',function(e){
  if(e.key===WL_KEY){ watchlistUI(); watchlistRender(); document.dispatchEvent(new CustomEvent('watchlistchange')); }
});
